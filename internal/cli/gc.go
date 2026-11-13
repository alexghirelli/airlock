package cli

import (
	"context"
	"fmt"

	"github.com/alexghirelli/airlock/internal/sandbox"
	"github.com/alexghirelli/airlock/internal/version"
)

// cmdGc lists what projects and releases that are gone left behind, and with
// --force removes it. Without --force nothing is touched.
func (e *env) cmdGc(ctx context.Context) (int, error) {
	res, err := e.loadUser()
	if err != nil {
		return 2, err
	}
	b, err := newBackend(&res.Config)
	if err != nil {
		return 1, err
	}
	if err := b.Ping(ctx); err != nil {
		return 1, err
	}
	plan, err := sandbox.CollectGC(ctx, b, "airlock:"+version.Tag(), nil)
	if err != nil {
		return 1, err
	}
	if plan.Empty() {
		fmt.Fprintln(e.stdout, "nothing to clean up")
		return 0, nil
	}
	verb := "would remove"
	if e.a.force {
		verb = "removing"
	}
	section := func(title string, items []sandbox.GCItem) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(e.stdout, "%s %s:\n", verb, title)
		for _, i := range items {
			fmt.Fprintf(e.stdout, "  %s  %s\n", i.Name, i.Detail)
		}
	}
	section("sandboxes whose project folder is gone", plan.Sandboxes)
	section("credentials volumes of projects that are gone", plan.Volumes)
	section("images no sandbox uses", plan.Images)
	if !e.a.force {
		e.errf("airlock: nothing was removed; run airlock gc --force to remove this (project files and shared logins are never touched)")
		return 0, nil
	}
	if err := sandbox.ApplyGC(ctx, b, plan); err != nil {
		return 1, err
	}
	return 0, nil
}
