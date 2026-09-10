// Package audit writes an append-only record of everything deckhand does.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event is one line in the audit log.
type Event struct {
	Time     time.Time `json:"time"`
	Watch    string    `json:"watch"`
	Repo     string    `json:"repo,omitempty"`
	Action   string    `json:"action"` // deploy, rollback, skip, halt, resume, error
	Result   string    `json:"result"` // ok, failed
	Ref      string    `json:"ref,omitempty"`
	SHA      string    `json:"sha,omitempty"`
	Command  string    `json:"command,omitempty"`
	ExitCode int       `json:"exit_code,omitempty"`
	Duration string    `json:"duration,omitempty"`
	Message  string    `json:"message,omitempty"`
}

// Log appends events to a JSON Lines file, rotating it when it grows past
// maxSize so a busy machine does not accumulate an unbounded history.
type Log struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	keep    int
}

// Open returns a log writing to dir/audit.jsonl. maxSize of 0 disables
// rotation; keep is how many rotated files to retain.
func Open(dir string, maxSize int64, keep int) (*Log, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Log{path: filepath.Join(dir, "audit.jsonl"), maxSize: maxSize, keep: keep}, nil
}

// Files returns the log files newest last, so a reader can replay the whole
// history across rotations.
func (l *Log) Files() []string {
	files := []string{}
	for i := l.keep; i >= 1; i-- {
		p := fmt.Sprintf("%s.%d", l.path, i)
		if _, err := os.Stat(p); err == nil {
			files = append(files, p)
		}
	}
	return append(files, l.path)
}

// rotate renames audit.jsonl to audit.jsonl.1, shifting older files up and
// dropping whatever falls off the end. The caller holds the lock.
func (l *Log) rotate() error {
	if l.keep <= 0 {
		return os.Remove(l.path)
	}
	oldest := fmt.Sprintf("%s.%d", l.path, l.keep)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return err
	}
	for i := l.keep - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", l.path, i)
		to := fmt.Sprintf("%s.%d", l.path, i+1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.Rename(l.path, l.path+".1")
}

// rotateIfNeeded rotates when the next write would exceed maxSize.
func (l *Log) rotateIfNeeded(incoming int) {
	if l.maxSize <= 0 {
		return
	}
	info, err := os.Stat(l.path)
	if err != nil {
		return
	}
	if info.Size()+int64(incoming) <= l.maxSize {
		return
	}
	_ = l.rotate()
}

// Path returns the file the log is written to.
func (l *Log) Path() string { return l.path }

// Write appends one event. Failures are reported but never fatal.
func (l *Log) Write(e Event) error {
	if l == nil {
		return nil
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rotateIfNeeded(len(data) + 1)
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}
