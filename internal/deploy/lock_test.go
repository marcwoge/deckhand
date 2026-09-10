package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireLock(dir, "shop")
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if _, err := AcquireLock(dir, "shop"); err == nil {
		t.Fatal("a second holder must be refused")
	} else if !strings.Contains(err.Error(), "another deckhand") {
		t.Errorf("error should say who holds it, got %v", err)
	}

	first.Release()
	second, err := AcquireLock(dir, "shop")
	if err != nil {
		t.Fatalf("after release the lock must be available: %v", err)
	}
	second.Release()
}

// A crash leaves the file behind. That must not wedge the worker forever.
func TestStaleLockFromDeadProcessIsCleared(t *testing.T) {
	dir := t.TempDir()
	host, _ := os.Hostname()
	stale := lockInfo{
		PID:   999999, // no such process
		Host:  host,
		Watch: "shop",
		Since: time.Now().Add(-time.Hour).UTC(),
	}
	data, _ := json.Marshal(stale)
	if err := os.WriteFile(filepath.Join(dir, "deploy.lock"), data, 0o640); err != nil {
		t.Fatal(err)
	}
	l, err := AcquireLock(dir, "shop")
	if err != nil {
		t.Fatalf("a lock from a dead process must be reclaimed: %v", err)
	}
	l.Release()
}

// A lock written by another machine cannot be judged, so it is left alone.
func TestLockFromAnotherHostIsRespected(t *testing.T) {
	dir := t.TempDir()
	data, _ := json.Marshal(lockInfo{PID: 1, Host: "some-other-machine",
		Watch: "shop", Since: time.Now().UTC()})
	_ = os.WriteFile(filepath.Join(dir, "deploy.lock"), data, 0o640)

	if _, err := AcquireLock(dir, "shop"); err == nil {
		t.Fatal("a lock from another host must be respected")
	}
}

func TestLockFromLiveProcessIsRespected(t *testing.T) {
	dir := t.TempDir()
	host, _ := os.Hostname()
	// Our own pid is definitely alive.
	data, _ := json.Marshal(lockInfo{PID: os.Getpid(), Host: host,
		Watch: "shop", Since: time.Now().UTC()})
	_ = os.WriteFile(filepath.Join(dir, "deploy.lock"), data, 0o640)

	if _, err := AcquireLock(dir, "shop"); err == nil {
		t.Fatal("a lock held by a running process must be respected")
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	l, err := AcquireLock(dir, "shop")
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	l.Release() // must not panic or remove someone else's lock
	other, err := AcquireLock(dir, "shop")
	if err != nil {
		t.Fatal(err)
	}
	l.Release() // the old handle must not take the new lock away
	if _, err := AcquireLock(dir, "shop"); err == nil {
		t.Error("the second lock was removed by a stale handle")
	}
	other.Release()
}
