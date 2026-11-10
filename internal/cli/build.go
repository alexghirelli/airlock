package cli

import (
	"context"
	"fmt"

	"airlock/internal/imagebuild"
	"airlock/internal/sandbox"
)

// cmdBuild builds the image locally; `update` rebuilds from scratch with the
// newest base layers and the pinned tool versions.
func (e *env) cmdBuild(ctx context.Context) (int, error) {
	var l *loaded
	var err error
	if e.a.path != "" || inProject() {
		l, err = e.load()
	} else {
		var res = mustUser(e)
		l = &loaded{res: res}
		err = nil
	}
	if err != nil {
		return 2, err
	}
	b, err := newBackend(&l.res.Config)
	if err != nil {
		return 1, err
	}
	if err := b.Ping(ctx); err != nil {
		return 1, err
	}
	m := &sandbox.Manager{B: b, Config: l.res, Project: l.project, Host: sandbox.DefaultHost(), Log: e.stderr, Verbose: e.a.verbose}
	mode := sandbox.BuildForce
	if e.a.command == "update" {
		mode = sandbox.BuildFresh
	}
	if l.project == nil {
		// No project: build only the base image.
		l.project = &sandbox.Project{Slug: "none"}
		m.Project = l.project
	}
	fmt.Fprintf(e.stderr, "airlock: building %s\n", imagebuild.BaseRef(&l.res.Config))
	plan, err := m.EnsureImage(ctx, mode)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(e.stdout, "%s\n", plan.Run)
	return 0, nil
}
