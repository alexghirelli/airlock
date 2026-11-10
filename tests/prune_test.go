//go:build integration

package tests

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/alexghirelli/airlock/internal/backend"
)

// Rebuilds must not pile up: untagged images airlock built are pruned, while
// untagged images it did not build are left alone.
func TestPruneImagesOnlyTouchesAirlockImages(t *testing.T) {
	build := func(label string) string {
		args := []string{"build", "-q"}
		if label != "" {
			args = append(args, "--label", label)
		}
		cmd := exec.Command("docker", append(args, "-")...)
		cmd.Stdin = strings.NewReader("FROM scratch\nLABEL nonce=" + t.Name() + label + "\n")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("docker build: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	ours := build(backend.LabelManaged + "=true")
	theirs := build("")
	t.Cleanup(func() { exec.Command("docker", "rmi", "-f", ours, theirs).Run() })

	d, err := backend.NewDocker("docker")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.PruneImages(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("docker", "image", "inspect", ours).Run(); err == nil {
		t.Errorf("untagged airlock image %s survived the prune", ours)
	}
	if err := exec.Command("docker", "image", "inspect", theirs).Run(); err != nil {
		t.Errorf("a foreign untagged image was pruned: %v", err)
	}
}
