package watcher

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcwoge/deckhand/internal/config"
)

func TestReportCounts(t *testing.T) {
	var buf bytes.Buffer
	r := &report{out: &buf}
	r.section("Environment")
	r.ok("git found")
	r.warn("running as root")
	r.fail("no such file")
	r.fail("cannot connect")

	if r.failed != 2 || r.warned != 1 {
		t.Errorf("failed=%d warned=%d, want 2 and 1", r.failed, r.warned)
	}
	out := buf.String()
	for _, want := range []string{"Environment", "ok    git found", "warn  running as root", "FAIL  no such file"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

func TestCheckPathReportsUnwritableTarget(t *testing.T) {
	e := testEngine(t)
	var buf bytes.Buffer
	r := &report{out: &buf}

	// A path under a regular file can never be created, even by root.
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := &config.Watch{Name: "broken", Path: filepath.Join(file, "sub"),
		Strategy: config.StrategyReleases}
	e.checkPath(r, w)

	if r.failed != 1 {
		t.Fatalf("an impossible path must be reported, output:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "cannot be created") {
		t.Errorf("output = %s", buf.String())
	}
}

func TestCheckPathAcceptsAGoodTarget(t *testing.T) {
	e := testEngine(t)
	var buf bytes.Buffer
	r := &report{out: &buf}

	w := &config.Watch{Name: "fine", Path: filepath.Join(t.TempDir(), "srv"),
		Strategy: config.StrategyReleases}
	e.checkPath(r, w)

	if r.failed != 0 {
		t.Fatalf("a writable path must pass, output:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "is writable") {
		t.Errorf("output = %s", buf.String())
	}
	// The probe must not leave anything behind.
	entries, err := os.ReadDir(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("doctor left files behind: %v", entries)
	}
}

func TestDoctorIsReadOnlyAboutTheHeartbeat(t *testing.T) {
	// Pinging during a check would make a dead worker look alive, so doctor
	// must only report the configuration.
	e := testEngine(t)
	var buf bytes.Buffer
	r := &report{out: &buf}
	e.cfg.Heartbeat = config.Heartbeat{URL: "http://127.0.0.1:1/ping", Interval: 0}
	e.checkNotifications(context.Background(), r)
	if !strings.Contains(buf.String(), "not pinged here") {
		t.Errorf("output should say the heartbeat is not pinged:\n%s", buf.String())
	}
}
