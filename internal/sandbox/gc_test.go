package sandbox

import (
	"context"
	"reflect"
	"testing"

	"github.com/alexghirelli/airlock/internal/backend"
)

type fakeGC struct {
	sandboxes []backend.Info
	volumes   []backend.VolumeInfo
	images    []string
	removed   []string
}

func (f *fakeGC) List(context.Context) ([]backend.Info, error)              { return f.sandboxes, nil }
func (f *fakeGC) ListVolumes(context.Context) ([]backend.VolumeInfo, error) { return f.volumes, nil }
func (f *fakeGC) ImageTags(context.Context, string) ([]string, error)       { return f.images, nil }
func (f *fakeGC) Remove(_ context.Context, n string, _ bool) error {
	f.removed = append(f.removed, "sandbox:"+n)
	return nil
}
func (f *fakeGC) RemoveVolume(_ context.Context, n string) error {
	f.removed = append(f.removed, "volume:"+n)
	return nil
}
func (f *fakeGC) RemoveImageTag(_ context.Context, n string) error {
	f.removed = append(f.removed, "image:"+n)
	return nil
}

func names(items []GCItem) []string {
	var out []string
	for _, i := range items {
		out = append(out, i.Name)
	}
	return out
}

func TestGCCollectsOnlyWhatIsGone(t *testing.T) {
	here := map[string]bool{"/work/alive": true, "/work/other/keep": true}
	proj := func(dir string) map[string]string { return map[string]string{backend.LabelProject: dir} }
	creds := func(dir string) map[string]string {
		return map[string]string{backend.LabelKind: backend.KindCredentials, backend.LabelProject: dir}
	}
	f := &fakeGC{
		sandboxes: []backend.Info{
			{Name: "airlock-alive-1", Image: "airlock:2", Labels: proj("/work/alive")},
			{Name: "airlock-gone-2", Image: "airlock:1", Labels: proj("/work/gone"), Running: true},
			{Name: "airlock-gone-wt-3", Image: "airlock:2", Labels: proj("/work/gone/.worktrees/.isolated/r")},
			{Name: "airlock-nolabel-4", Image: "airlock:2"},
			// The project folder is gone but this worktree is somewhere else and still there.
			{Name: "airlock-other-5", Image: "airlock:2", Labels: proj("/work/other/keep")},
		},
		volumes: []backend.VolumeInfo{
			{Name: "airlock-creds", Labels: map[string]string{backend.LabelManaged: "true"}, InUse: true},      // shared logins
			{Name: "airlock-tools", Labels: map[string]string{backend.LabelManaged: "true"}, InUse: true},      // tools
			{Name: "airlock-creds-alive", Labels: creds("/work/alive"), InUse: true},                           // project is there
			{Name: "airlock-creds-gone", Labels: creds("/work/gone"), InUse: true},                             // its sandboxes go too
			{Name: "airlock-creds-gone-free", Labels: creds("/work/gone")},                                     // nobody mounts it
			{Name: "airlock-creds-old", Labels: map[string]string{backend.LabelManaged: "true"}, InUse: false}, // made before labels
			{Name: "airlock-creds-other", Labels: creds("/work/other"), InUse: true},                           // held by a sandbox that stays
		},
		images: []string{"airlock:2", "airlock:1", "airlock:0.9", "airlock:3", "airlock:3-1a2b3c4d"},
	}

	plan, err := CollectGC(context.Background(), f, "airlock:3", func(p string) bool { return here[p] })
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(plan.Sandboxes), []string{"airlock-gone-2", "airlock-gone-wt-3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sandboxes: got %v, want %v", got, want)
	}
	if got, want := names(plan.Volumes), []string{"airlock-creds-gone", "airlock-creds-gone-free"}; !reflect.DeepEqual(got, want) {
		t.Errorf("volumes: got %v, want %v", got, want)
	}
	// airlock:2 is used, airlock:3 is the current release; 1 is used by a sandbox that is
	// going away, but this run does not know yet, so it stays until the next one.
	if got, want := names(plan.Images), []string{"airlock:0.9"}; !reflect.DeepEqual(got, want) {
		t.Errorf("images: got %v, want %v", got, want)
	}

	if err := ApplyGC(context.Background(), f, plan); err != nil {
		t.Fatal(err)
	}
	want := []string{"sandbox:airlock-gone-2", "sandbox:airlock-gone-wt-3", "volume:airlock-creds-gone", "volume:airlock-creds-gone-free", "image:airlock:0.9"}
	if !reflect.DeepEqual(f.removed, want) {
		t.Errorf("removed: got %v, want %v", f.removed, want)
	}
}

func TestGCWithNothingToDo(t *testing.T) {
	plan, err := CollectGC(context.Background(), &fakeGC{}, "airlock:3", func(string) bool { return true })
	if err != nil || !plan.Empty() {
		t.Errorf("plan %+v, err %v", plan, err)
	}
}
