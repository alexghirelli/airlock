package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/alexghirelli/airlock/internal/backend"
)

type lsEntry struct {
	Name    string `json:"name"`
	Project string `json:"project"`
	Status  string `json:"status"`
	Running bool   `json:"running"`
	Image   string `json:"image"`
	Backend string `json:"backend"`
	Uptime  string `json:"uptime,omitempty"`
}

func (e *env) cmdLs(ctx context.Context) (int, error) {
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
	list, err := b.List(ctx)
	if err != nil {
		return 1, err
	}
	entries := make([]lsEntry, 0, len(list))
	for _, i := range list {
		entries = append(entries, lsEntry{
			Name: i.Name, Project: i.Labels[backend.LabelProject], Status: i.Status, Running: i.Running,
			Image: i.Image, Backend: i.Labels[backend.LabelBackend], Uptime: uptime(i),
		})
	}
	if e.a.json {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return 0, enc.Encode(entries)
	}
	if len(entries) == 0 {
		fmt.Fprintln(e.stdout, "no sandboxes yet; run airlock in a project folder")
		return 0, nil
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tSTATUS\tBACKEND\tIMAGE\tUPTIME\tNAME")
	for _, x := range entries {
		state := "stopped"
		if x.Running {
			state = "running"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", x.Project, state, x.Backend, x.Image, x.Uptime, x.Name)
	}
	return 0, tw.Flush()
}

func uptime(i backend.Info) string {
	if !i.Running {
		return ""
	}
	// List only knows the status text ("Up 3 hours"); prefer it.
	if s, ok := strings.CutPrefix(i.Status, "Up "); ok {
		return s
	}
	if !i.StartedAt.IsZero() {
		return time.Since(i.StartedAt).Round(time.Minute).String()
	}
	return ""
}
