package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotationKeepsBoundedHistory(t *testing.T) {
	dir := t.TempDir()
	// Small enough that a couple of events fill it.
	l, err := Open(dir, 400, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := l.Write(Event{Watch: "shop", Action: "deploy", Result: "ok",
			Message: strings.Repeat("x", 100)}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 3 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("keep=2 should leave at most 3 files, found %v", names)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit.jsonl.1")); err != nil {
		t.Errorf("no rotated file was created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "audit.jsonl.3")); err == nil {
		t.Error("a file beyond keep=2 survived")
	}
}

// Files() drives history output, so the order decides whether the log reads
// chronologically across a rotation.
func TestFilesAreOldestFirst(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, 200, 3)
	for i := 0; i < 15; i++ {
		_ = l.Write(Event{Watch: "shop", Message: strings.Repeat("y", 80)})
	}
	files := l.Files()
	if len(files) < 2 {
		t.Fatalf("expected rotated files, got %v", files)
	}
	if !strings.HasSuffix(files[len(files)-1], "audit.jsonl") {
		t.Errorf("the live file must come last, got %v", files)
	}
	// Higher numbers are older, so they must come first.
	if !strings.HasSuffix(files[0], ".2") && !strings.HasSuffix(files[0], ".3") {
		t.Errorf("oldest rotated file should be first, got %v", files)
	}
}

func TestRotationDisabled(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, 0, 5)
	for i := 0; i < 50; i++ {
		_ = l.Write(Event{Watch: "shop", Message: strings.Repeat("z", 200)})
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("max_size 0 means never rotate, found %d files", len(entries))
	}
}
