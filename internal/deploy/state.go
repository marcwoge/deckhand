package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// State is the on-disk memory of a watch. It lives next to the deployment so
// deckhand can be restarted, moved or reinstalled without redeploying.
type State struct {
	Watch           string `json:"watch"`
	Repo            string `json:"repo"`
	LastSHA         string `json:"last_sha,omitempty"`
	LastRef         string `json:"last_ref,omitempty"`
	CurrentRelease  string `json:"current_release,omitempty"`
	PreviousRelease string `json:"previous_release,omitempty"`
	PreviousSHA     string `json:"previous_sha,omitempty"`
	// ActiveSHA is what is physically live, which after a failed deployment
	// that could not be rolled back is not the same as LastSHA.
	ActiveSHA     string    `json:"active_sha,omitempty"`
	ActiveRelease string    `json:"active_release,omitempty"`
	LastAttempt   time.Time `json:"last_attempt,omitempty"`
	LastSuccess   time.Time `json:"last_success,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	Failures      int       `json:"failures"`
	Halted        bool      `json:"halted"`

	path string
}

// LoadState reads the state file, returning an empty state when absent.
func LoadState(dir, watch, repo string) (*State, error) {
	s := &State{Watch: watch, Repo: repo, path: filepath.Join(dir, "state.json")}
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		// A corrupt state file must not stop the daemon; treat it as fresh.
		return &State{Watch: watch, Repo: repo, path: s.path}, nil
	}
	s.Watch, s.Repo = watch, repo
	return s, nil
}

// Save writes the state atomically.
func (s *State) Save() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
