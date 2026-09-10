package deploy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Lock is a cross-process guard around one watch's deployment directory.
//
// The engine already serialises deployments inside one process. This covers
// the other case: a service running as the deckhand user and an operator
// running "deckhand deploy" by hand, both pointed at the same path.
type Lock struct {
	path string
	held bool
}

// lockInfo is written into the lock file so a stale lock can be explained
// rather than just reported.
type lockInfo struct {
	PID   int       `json:"pid"`
	Host  string    `json:"host"`
	Watch string    `json:"watch"`
	Since time.Time `json:"since"`
}

// ErrLocked reports that someone else is deploying this watch.
type ErrLocked struct {
	Info lockInfo
	Path string
}

func (e *ErrLocked) Error() string {
	return fmt.Sprintf("another deckhand is deploying %q (pid %d on %s, since %s); "+
		"wait for it to finish, or remove %s if that process is gone",
		e.Info.Watch, e.Info.PID, e.Info.Host, e.Info.Since.Format(time.RFC3339), e.Path)
}

// AcquireLock takes the lock for a watch, or reports who holds it.
func AcquireLock(dir, watch string) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "deploy.lock")
	l := &Lock{path: path}

	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if err == nil {
			host, _ := os.Hostname()
			info := lockInfo{PID: os.Getpid(), Host: host, Watch: watch, Since: time.Now().UTC()}
			data, _ := json.Marshal(info)
			_, _ = f.Write(data)
			f.Close()
			l.held = true
			return l, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		// The file exists. Decide whether the holder is still alive.
		info, readErr := readLockInfo(path)
		if readErr != nil || isStale(info) {
			// A crash leaves the file behind; clearing it must not wedge the
			// worker forever. Only the first attempt may clear it, so two
			// racing processes cannot both decide the lock is theirs.
			if attempt == 0 {
				_ = os.Remove(path)
				continue
			}
		}
		return nil, &ErrLocked{Info: info, Path: path}
	}
	return nil, &ErrLocked{Path: path}
}

func readLockInfo(path string) (lockInfo, error) {
	var info lockInfo
	data, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return info, err
	}
	return info, nil
}

// isStale reports whether the recorded holder is definitely gone. A lock from
// another host cannot be judged, so it is treated as live.
func isStale(info lockInfo) bool {
	host, _ := os.Hostname()
	if info.Host != host || info.PID <= 0 {
		return false
	}
	return !processAlive(info.PID)
}

// Release removes the lock. It is safe to call more than once.
func (l *Lock) Release() {
	if l == nil || !l.held {
		return
	}
	l.held = false
	_ = os.Remove(l.path)
}
