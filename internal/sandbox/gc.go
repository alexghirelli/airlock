package sandbox

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/alexghirelli/airlock/internal/backend"
)

// gcBackend is what garbage collection needs from a backend.
type gcBackend interface {
	List(ctx context.Context) ([]backend.Info, error)
	ListVolumes(ctx context.Context) ([]backend.VolumeInfo, error)
	ImageTags(ctx context.Context, repo string) ([]string, error)
	Remove(ctx context.Context, name string, force bool) error
	RemoveVolume(ctx context.Context, name string) error
	RemoveImageTag(ctx context.Context, ref string) error
}

// GCItem is one thing `airlock gc` would remove.
type GCItem struct {
	Name   string // container, volume or image
	Detail string // the project folder that is gone, or why the image is stale
}

// GCPlan is what is left behind by projects and releases that are gone.
type GCPlan struct {
	Sandboxes []GCItem // sandboxes whose project folder no longer exists
	Volumes   []GCItem // a project's own credentials volume, nobody mounts it, project gone
	Images    []GCItem // airlock images of an older release that no sandbox uses
}

// Empty reports whether there is nothing to remove.
func (p *GCPlan) Empty() bool { return len(p.Sandboxes)+len(p.Volumes)+len(p.Images) == 0 }

// CollectGC finds what is no longer needed. currentImage is the base image of this
// release ("airlock:0.2.1"), which is never collected; exists says whether a path is
// there on this machine. Nothing is touched: ApplyGC removes what this returns.
//
// It only ever suggests things that tell you they are leftovers: a sandbox or a
// credentials volume labelled with a project folder that does not exist any more
// (volumes made before airlock labelled them are never collected), and `airlock:`
// images that no sandbox uses. Shared logins and the tools volume are not touched.
func CollectGC(ctx context.Context, b gcBackend, currentImage string, exists func(string) bool) (*GCPlan, error) {
	if exists == nil {
		exists = func(p string) bool { _, err := os.Stat(p); return err == nil }
	}
	sandboxes, err := b.List(ctx)
	if err != nil {
		return nil, err
	}
	plan := &GCPlan{}
	used := map[string]bool{} // images some sandbox is made from
	for _, s := range sandboxes {
		used[s.Image] = true
		if dir := s.Labels[backend.LabelProject]; dir != "" && !exists(dir) {
			state := "stopped"
			if s.Running {
				state = "running"
			}
			plan.Sandboxes = append(plan.Sandboxes, GCItem{Name: s.Name, Detail: dir + " (" + state + ")"})
		}
	}

	volumes, err := b.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}
	gone := map[string]bool{} // sandboxes this run removes do not hold their volume
	for _, s := range plan.Sandboxes {
		gone[s.Name] = true
	}
	for _, v := range volumes {
		dir := v.Labels[backend.LabelProject]
		if v.Labels[backend.LabelKind] != backend.KindCredentials || dir == "" || exists(dir) {
			continue
		}
		if v.InUse && !heldOnlyBy(v, sandboxes, gone) {
			continue
		}
		plan.Volumes = append(plan.Volumes, GCItem{Name: v.Name, Detail: dir})
	}

	tags, err := b.ImageTags(ctx, "airlock")
	if err != nil {
		return nil, err
	}
	for _, t := range tags {
		// This release's image, and its variants with the extra packages of a config
		// ("airlock:0.2.1-<hash>"), are kept even when no sandbox exists yet.
		if t == currentImage || strings.HasPrefix(t, currentImage+"-") || used[t] {
			continue
		}
		plan.Images = append(plan.Images, GCItem{Name: t, Detail: "no sandbox uses it"})
	}
	return plan, nil
}

// heldOnlyBy is true when every sandbox that could mount the volume is being
// removed. The engine does not say which sandbox holds a volume, so this is
// decided by the volume's project: its sandboxes are the ones labelled with it.
func heldOnlyBy(v backend.VolumeInfo, all []backend.Info, gone map[string]bool) bool {
	dir := v.Labels[backend.LabelProject]
	for _, s := range all {
		if s.Labels[backend.LabelProject] == dir || strings.HasPrefix(s.Labels[backend.LabelProject], dir+"/") {
			if !gone[s.Name] {
				return false
			}
		}
	}
	return true
}

// ApplyGC removes what CollectGC found, sandboxes first so their volumes are free,
// and returns the first error after trying everything.
func ApplyGC(ctx context.Context, b gcBackend, p *GCPlan) error {
	var errs []error
	for _, s := range p.Sandboxes {
		if err := b.Remove(ctx, s.Name, true); err != nil && !errors.Is(err, backend.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	for _, v := range p.Volumes {
		if err := b.RemoveVolume(ctx, v.Name); err != nil {
			errs = append(errs, err)
		}
	}
	for _, i := range p.Images {
		if err := b.RemoveImageTag(ctx, i.Name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
