package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteHandoffReplacesAndClears(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rate-limit")
	if err := WriteHandoff(dir, "first\n"); err != nil {
		t.Fatal(err)
	}
	if err := WriteHandoff(dir, "second\n"); err != nil {
		t.Fatal(err)
	}
	if got := readTask(dir, handoffFile); got != "second\n" {
		t.Errorf("handoff = %q", got)
	}
	if err := ClearHandoff(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, handoffFile)); !os.IsNotExist(err) {
		t.Errorf("handoff still there after clearing: %v", err)
	}
	// Clearing twice is not an error.
	if err := ClearHandoff(dir); err != nil {
		t.Errorf("second clear: %v", err)
	}
}

func TestClearHandoffKeepsTheFocus(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rate-limit")
	if err := SetFocus(dir, "keep"); err != nil {
		t.Fatal(err)
	}
	if err := WriteHandoff(dir, "x\n"); err != nil {
		t.Fatal(err)
	}
	if err := ClearHandoff(dir); err != nil {
		t.Fatal(err)
	}
	if Focus(dir) != "keep" {
		t.Errorf("focus lost by clearing the handoff: %q", Focus(dir))
	}
}

func TestRemoveTasksDeletesTheWholeFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rate-limit")
	if err := SetFocus(dir, "x"); err != nil {
		t.Fatal(err)
	}
	if err := WriteHandoff(dir, "y\n"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTasks(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("task folder still there: %v", err)
	}
	if err := RemoveTasks(""); err == nil {
		t.Error("removing with no folder must fail")
	}
}
