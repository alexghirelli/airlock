package backend

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/alexghirelli/airlock/internal/termio"
)

// Docker is the container backend: a hardened OCI container driven through
// the Docker Engine API. It also serves the gvisor backend, which only
// differs in the OCI runtime (runsc).
type Docker struct {
	cli  *client.Client
	name string
}

// NewDocker connects to the engine named by the usual DOCKER_* environment
// (Docker Engine, Docker Desktop, OrbStack, Colima, Podman's compatible socket).
func NewDocker(name string) (*Docker, error) {
	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("connect to the container engine: %w", err)
	}
	return &Docker{cli: cli, name: name}, nil
}

func (d *Docker) Name() string { return d.name }

// Close releases the engine connection.
func (d *Docker) Close() error { return d.cli.Close() }

func (d *Docker) Ping(ctx context.Context) error {
	if _, err := d.cli.Ping(ctx, client.PingOptions{}); err != nil {
		return fmt.Errorf("cannot reach the container engine (is Docker running?): %w", err)
	}
	return nil
}

func (d *Docker) Info(ctx context.Context) (*EngineInfo, error) {
	r, err := d.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return nil, err
	}
	i := r.Info
	out := &EngineInfo{
		Name:          i.Name,
		Version:       i.ServerVersion,
		OS:            i.OperatingSystem,
		Arch:          i.Architecture,
		DockerDesktop: strings.Contains(strings.ToLower(i.OperatingSystem), "docker desktop"),
	}
	for _, o := range i.SecurityOptions {
		switch {
		case strings.Contains(o, "name=rootless"):
			out.Rootless = true
		case strings.Contains(o, "name=selinux"):
			out.SELinux = true
		case strings.Contains(o, "name=userns"):
			out.UserNS = true
		}
	}
	for name := range i.Runtimes {
		out.Runtimes = append(out.Runtimes, name)
	}
	sort.Strings(out.Runtimes)
	return out, nil
}

func (d *Docker) EnsureNetwork(ctx context.Context, name string) error {
	_, err := d.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err == nil {
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		return err
	}
	// A user-defined network gives the sandbox Docker's embedded DNS
	// (127.0.0.11), which keeps working while the firewall blocks private
	// address ranges.
	_, err = d.cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver: "bridge",
		Labels: map[string]string{LabelManaged: "true"},
	})
	if err != nil && !cerrdefs.IsConflict(err) {
		return fmt.Errorf("create network %s: %w", name, err)
	}
	return nil
}

func (d *Docker) EnsureVolume(ctx context.Context, name string) error {
	_, err := d.cli.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name:   name,
		Labels: map[string]string{LabelManaged: "true"},
	})
	return err
}

func (d *Docker) RemoveVolume(ctx context.Context, name string) error {
	_, err := d.cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return err
	}
	return nil
}

func (d *Docker) ImageExists(ctx context.Context, ref string) (bool, error) {
	_, err := d.cli.ImageInspect(ctx, ref)
	if err == nil {
		return true, nil
	}
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	return false, err
}

func (d *Docker) ImageTags(ctx context.Context, repo string) ([]string, error) {
	r, err := d.cli.ImageList(ctx, client.ImageListOptions{Filters: client.Filters{}.Add("reference", repo)})
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, img := range r.Items {
		for _, t := range img.RepoTags {
			if strings.HasPrefix(t, repo+":") {
				tags = append(tags, t)
			}
		}
	}
	return tags, nil
}

func (d *Docker) RemoveImageTag(ctx context.Context, ref string) error {
	_, err := d.cli.ImageRemove(ctx, ref, client.ImageRemoveOptions{PruneChildren: true})
	if err != nil && !cerrdefs.IsNotFound(err) && !cerrdefs.IsConflict(err) {
		return err
	}
	return nil
}

func (d *Docker) PruneImages(ctx context.Context) error {
	f := client.Filters{}.Add("dangling", "true").Add("label", LabelManaged+"=true")
	_, err := d.cli.ImagePrune(ctx, client.ImagePruneOptions{Filters: f})
	return err
}

func (d *Docker) BuildImage(ctx context.Context, req BuildRequest) error {
	args := map[string]*string{}
	for k, v := range req.Args {
		v := v
		args[k] = &v
	}
	resp, err := d.cli.ImageBuild(ctx, req.Context, client.ImageBuildOptions{
		Tags:       req.Tags,
		Dockerfile: req.Dockerfile,
		BuildArgs:  args,
		NoCache:    req.NoCache,
		PullParent: req.Pull,
		Remove:     true,
		Labels:     map[string]string{LabelManaged: "true"},
	})
	if err != nil {
		return fmt.Errorf("build %s: %w", strings.Join(req.Tags, ","), err)
	}
	defer resp.Body.Close()
	return readBuildStream(resp.Body, req.Progress)
}

// readBuildStream relays the engine's JSON build log and returns its error.
func readBuildStream(r io.Reader, progress io.Writer) error {
	if progress == nil {
		progress = io.Discard
	}
	dec := json.NewDecoder(r)
	for {
		var msg struct {
			Stream      string `json:"stream"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if msg.Stream != "" {
			fmt.Fprint(progress, msg.Stream)
		}
		if msg.Error != "" {
			if msg.ErrorDetail.Message != "" {
				return errors.New(msg.ErrorDetail.Message)
			}
			return errors.New(msg.Error)
		}
	}
}

func (d *Docker) Create(ctx context.Context, s Spec) error {
	hc := &container.HostConfig{
		NetworkMode: container.NetworkMode(s.Network),
		CapDrop:     []string{"ALL"},
		CapAdd:      s.CapAdd,
		SecurityOpt: s.SecurityOpt,
		Tmpfs:       s.Tmpfs,
		Runtime:     s.Runtime,
		AutoRemove:  s.AutoRemove,
		Resources: container.Resources{
			Memory:     s.MemoryBytes,
			MemorySwap: s.MemoryBytes, // no swap: the limit is the limit
			NanoCPUs:   s.NanoCPUs,
		},
	}
	if s.Pids > 0 {
		pids := s.Pids
		hc.PidsLimit = &pids
	}
	if s.Init {
		t := true
		hc.Init = &t
	}
	// Mounts (unlike Binds) fail when the source is missing instead of
	// creating a root-owned directory on the host.
	for _, b := range s.Binds {
		hc.Mounts = append(hc.Mounts, mount.Mount{
			Type: mount.TypeBind, Source: b.Source, Target: b.Target, ReadOnly: b.ReadOnly,
			BindOptions: &mount.BindOptions{CreateMountpoint: false},
		})
	}
	for _, v := range s.Volumes {
		hc.Mounts = append(hc.Mounts, mount.Mount{Type: mount.TypeVolume, Source: v.Name, Target: v.Target})
	}
	cfg := &container.Config{
		Image:      s.Image,
		Hostname:   s.Hostname,
		WorkingDir: s.WorkDir,
		Env:        s.Env,
		Labels:     s.Labels,
		Cmd:        s.Cmd,
	}
	nc := &network.NetworkingConfig{}
	_, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: s.Name, Config: cfg, HostConfig: hc, NetworkingConfig: nc,
	})
	if err != nil {
		return fmt.Errorf("create %s: %w", s.Name, err)
	}
	return nil
}

func (d *Docker) Start(ctx context.Context, name string) error {
	_, err := d.cli.ContainerStart(ctx, name, client.ContainerStartOptions{})
	return err
}

func (d *Docker) Stop(ctx context.Context, name string) error {
	timeout := 10
	_, err := d.cli.ContainerStop(ctx, name, client.ContainerStopOptions{Timeout: &timeout})
	if cerrdefs.IsNotFound(err) {
		return ErrNotFound
	}
	return err
}

func (d *Docker) Remove(ctx context.Context, name string, force bool) error {
	_, err := d.cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: force})
	if cerrdefs.IsNotFound(err) {
		return ErrNotFound
	}
	return err
}

func (d *Docker) CopyTo(ctx context.Context, name string, files []File) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dirs := map[string]bool{}
	for _, f := range files {
		rel := strings.TrimPrefix(path.Clean(f.Path), "/")
		// Parent directories first, so nested paths can be extracted.
		parts := strings.Split(path.Dir(rel), "/")
		for i := range parts {
			dir := strings.Join(parts[:i+1], "/")
			if dir == "." || dirs[dir] {
				continue
			}
			dirs[dir] = true
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: dir + "/", Mode: 0o755}); err != nil {
				return err
			}
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: rel, Mode: mode, Size: int64(len(f.Data)), ModTime: time.Now()}); err != nil {
			return err
		}
		if _, err := tw.Write(f.Data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	_, err := d.cli.CopyToContainer(ctx, name, client.CopyToContainerOptions{DestinationPath: "/", Content: &buf})
	if err != nil {
		return fmt.Errorf("copy files into %s: %w", name, err)
	}
	return nil
}

func (d *Docker) Logs(ctx context.Context, name string, tail int) (string, error) {
	r, err := d.cli.ContainerLogs(ctx, name, client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Tail: fmt.Sprint(tail),
	})
	if err != nil {
		return "", err
	}
	defer r.Close()
	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, r); err != nil {
		// A container with a TTY has an unmultiplexed log.
		return out.String(), nil
	}
	return out.String(), nil
}

func (d *Docker) Exec(ctx context.Context, name string, o ExecOpts) (int, error) {
	return d.exec(ctx, name, o, false)
}

func (d *Docker) Attach(ctx context.Context, name string, o ExecOpts) (int, error) {
	o.TTY = true
	return d.exec(ctx, name, o, true)
}

func (d *Docker) exec(ctx context.Context, name string, o ExecOpts, interactive bool) (int, error) {
	stdin := o.Stdin
	stdout, stderr := o.Stdout, o.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	var term *termio.Terminal
	if interactive {
		t, err := termio.Open()
		if err != nil {
			return 1, err
		}
		term = t
		stdin, stdout, stderr = t.In, t.Out, t.Out
	}

	create := client.ExecCreateOptions{
		User: o.User, TTY: o.TTY, Env: o.Env, WorkingDir: o.WorkDir, Cmd: o.Cmd,
		AttachStdin: stdin != nil, AttachStdout: true, AttachStderr: true,
	}
	if term != nil {
		w, h := term.Size()
		create.ConsoleSize = client.ConsoleSize{Height: uint(h), Width: uint(w)}
	}
	ex, err := d.cli.ExecCreate(ctx, name, create)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return 1, ErrNotFound
		}
		return 1, err
	}
	att, err := d.cli.ExecAttach(ctx, ex.ID, client.ExecAttachOptions{TTY: o.TTY, ConsoleSize: create.ConsoleSize})
	if err != nil {
		return 1, err
	}
	defer att.Close()

	if term != nil {
		restore, err := term.MakeRaw()
		if err != nil {
			return 1, err
		}
		defer restore()
		stop := term.WatchResize(func(w, h int) {
			_, _ = d.cli.ExecResize(ctx, ex.ID, client.ExecResizeOptions{Height: uint(h), Width: uint(w)})
		})
		defer stop()
	}

	if stdin != nil {
		go func() {
			_, _ = io.Copy(att.Conn, stdin)
			if cw, ok := att.Conn.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
		}()
	}

	copyDone := make(chan error, 1)
	go func() {
		var err error
		if o.TTY {
			_, err = io.Copy(stdout, att.Reader)
		} else {
			_, err = stdcopy.StdCopy(stdout, stderr, att.Reader)
		}
		copyDone <- err
	}()
	select {
	case err := <-copyDone:
		if err != nil && !errors.Is(err, io.EOF) && ctx.Err() == nil {
			return 1, err
		}
	case <-ctx.Done():
		return 1, ctx.Err()
	}

	// The exit code is available shortly after the stream closes.
	for i := 0; i < 50; i++ {
		res, err := d.cli.ExecInspect(ctx, ex.ID, client.ExecInspectOptions{})
		if err != nil {
			return 1, err
		}
		if !res.Running {
			return res.ExitCode, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 1, errors.New("command did not report an exit status")
}

func (d *Docker) Inspect(ctx context.Context, name string) (*Info, error) {
	r, err := d.cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c := r.Container
	info := &Info{ID: c.ID, Name: strings.TrimPrefix(c.Name, "/"), Image: c.Image}
	if c.Config != nil {
		info.Labels = c.Config.Labels
		info.Env = c.Config.Env
		if c.Config.Image != "" {
			info.Image = c.Config.Image
		}
	}
	if c.State != nil {
		info.Running = c.State.Running
		info.Status = string(c.State.Status)
		if t, err := time.Parse(time.RFC3339Nano, c.State.StartedAt); err == nil && c.State.Running {
			info.StartedAt = t
		}
	}
	return info, nil
}

func (d *Docker) List(ctx context.Context) ([]Info, error) {
	f := client.Filters{}.Add("label", LabelManaged+"=true")
	r, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, c := range r.Items {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, Info{
			ID: c.ID, Name: name, Image: c.Image, Labels: c.Labels,
			Running: string(c.State) == "running", Status: c.Status,
			StartedAt: time.Unix(c.Created, 0),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
