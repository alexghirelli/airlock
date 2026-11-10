// Package backend defines how airlock drives an isolation backend, and the
// container backend that implements it through the Docker Engine API.
//
// Every backend exposes the same operations, so the same image, mounts,
// volumes, egress policy and CLI work on all of them (see docs/backends.md).
package backend

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound is returned when a sandbox does not exist.
var ErrNotFound = errors.New("sandbox not found")

// Label keys set on every sandbox airlock creates.
const (
	LabelManaged  = "io.airlock.managed"
	LabelProject  = "io.airlock.project"
	LabelSlug     = "io.airlock.slug"
	LabelBackend  = "io.airlock.backend"
	LabelVersion  = "io.airlock.version"
	LabelPolicy   = "io.airlock.policy"
	LabelLifetime = "io.airlock.lifetime"
)

// Bind is a host path mounted into the sandbox.
type Bind struct {
	Source   string
	Target   string
	ReadOnly bool
}

// VolumeMount is a named volume mounted into the sandbox.
type VolumeMount struct {
	Name   string
	Target string
}

// Spec describes a sandbox to create.
type Spec struct {
	Name        string
	Image       string
	Hostname    string
	WorkDir     string
	Labels      map[string]string
	Env         []string
	Binds       []Bind
	Volumes     []VolumeMount
	Tmpfs       map[string]string
	Network     string
	CapAdd      []string
	SecurityOpt []string
	MemoryBytes int64
	NanoCPUs    int64
	Pids        int64
	Runtime     string // OCI runtime, e.g. runsc for gVisor
	Init        bool
	AutoRemove  bool
	Cmd         []string
}

// Info describes an existing sandbox.
type Info struct {
	ID        string
	Name      string
	Image     string
	Running   bool
	Status    string
	StartedAt time.Time
	Labels    map[string]string
	Env       []string
}

// File is a regular file to place inside a sandbox.
type File struct {
	Path string // absolute path inside the sandbox
	Data []byte
	Mode int64
}

// ExecOpts configures Exec and Attach.
type ExecOpts struct {
	Cmd     []string
	Env     []string
	WorkDir string
	User    string
	TTY     bool
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

// BuildRequest builds an image from a tar build context.
type BuildRequest struct {
	Context    io.Reader
	Tags       []string
	Dockerfile string
	Args       map[string]string
	NoCache    bool
	Pull       bool
	Progress   io.Writer
}

// EngineInfo is what `airlock doctor` needs to know about the runtime.
type EngineInfo struct {
	Name          string
	Version       string
	OS            string
	Arch          string
	Rootless      bool
	SELinux       bool
	Runtimes      []string
	DockerDesktop bool
	UserNS        bool
}

// Backend is the pluggable isolation backend.
type Backend interface {
	Name() string
	Ping(ctx context.Context) error
	Info(ctx context.Context) (*EngineInfo, error)

	EnsureNetwork(ctx context.Context, name string) error
	EnsureVolume(ctx context.Context, name string) error
	RemoveVolume(ctx context.Context, name string) error

	ImageExists(ctx context.Context, ref string) (bool, error)
	// ImageTags lists the tags of local images in a repository.
	ImageTags(ctx context.Context, repo string) ([]string, error)
	// RemoveImageTag untags an image; images in use by a container are kept.
	RemoveImageTag(ctx context.Context, ref string) error
	// PruneImages deletes untagged images that airlock built and no container
	// uses. Images airlock did not build are never touched.
	PruneImages(ctx context.Context) error
	BuildImage(ctx context.Context, req BuildRequest) error

	Create(ctx context.Context, spec Spec) error
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string) error
	Remove(ctx context.Context, name string, force bool) error
	CopyTo(ctx context.Context, name string, files []File) error
	Logs(ctx context.Context, name string, tail int) (string, error)

	// Exec runs a command and returns its exit code. Attach is Exec on the
	// user's terminal: it forces a TTY, puts the terminal in raw mode and
	// forwards resizes.
	Exec(ctx context.Context, name string, opts ExecOpts) (int, error)
	Attach(ctx context.Context, name string, opts ExecOpts) (int, error)

	Inspect(ctx context.Context, name string) (*Info, error)
	List(ctx context.Context) ([]Info, error)
}
