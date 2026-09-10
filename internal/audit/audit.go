// Package audit writes an append-only record of everything deckhand does.
package audit

import (
	"encoding/json"
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

// Log appends events to a JSON Lines file.
type Log struct {
	mu   sync.Mutex
	path string
}

// Open returns a log writing to dir/audit.jsonl.
func Open(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Log{path: filepath.Join(dir, "audit.jsonl")}, nil
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
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}
