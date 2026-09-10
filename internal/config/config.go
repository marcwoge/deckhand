// Package config loads and validates deckhand.yaml.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	StrategyReleases = "releases"
	StrategyInplace  = "inplace"

	TriggerRelease = "release"
	TriggerBranch  = "branch"
	TriggerTag     = "tag"
)

// Config is the whole deckhand.yaml file.
type Config struct {
	Version  int      `yaml:"version"`
	Defaults Defaults `yaml:"defaults"`
	GitHub   GitHub   `yaml:"github"`
	Notify   Notify   `yaml:"notify"`
	Watches  []*Watch `yaml:"watch"`

	// Path is the file this config was read from (not part of the YAML).
	Path string `yaml:"-"`
}

// Defaults apply to every watch that does not override them.
type Defaults struct {
	PollInterval   Duration          `yaml:"poll_interval"`
	Timezone       string            `yaml:"timezone"`
	CommandTimeout Duration          `yaml:"command_timeout"`
	Strategy       string            `yaml:"strategy"`
	KeepReleases   int               `yaml:"keep_releases"`
	FailureLimit   int               `yaml:"failure_limit"`
	StateDir       string            `yaml:"state_dir"`
	Env            map[string]string `yaml:"env"`
}

// GitHub holds API access settings. Prefer TokenEnv or TokenFile over Token.
type GitHub struct {
	Token     string `yaml:"token"`
	TokenEnv  string `yaml:"token_env"`
	TokenFile string `yaml:"token_file"`
	API       string `yaml:"api"`
	// Host is the git host used for cloning; change it for GitHub Enterprise.
	Host string `yaml:"host"`
}

// Notify configures outbound notifications.
type Notify struct {
	On      []string `yaml:"on"` // success, failure, rollback, skip
	Webhook string   `yaml:"webhook"`
	Format  string   `yaml:"format"` // json (default), slack, ntfy
}

// Watch is a single repository being observed.
type Watch struct {
	Name    string  `yaml:"name"`
	Repo    string  `yaml:"repo"` // owner/name
	Auth    string  `yaml:"auth"` // token (default) | none
	Trigger Trigger `yaml:"trigger"`
	Path    string  `yaml:"path"`

	Strategy       string            `yaml:"strategy"`
	Shared         []string          `yaml:"shared"`
	KeepReleases   int               `yaml:"keep_releases"`
	PollInterval   Duration          `yaml:"poll_interval"`
	CommandTimeout Duration          `yaml:"command_timeout"`
	FailureLimit   int               `yaml:"failure_limit"`
	Env            map[string]string `yaml:"env"`

	Window    *Window   `yaml:"window"`
	Verify    Verify    `yaml:"verify"`
	Run       []Command `yaml:"run"`
	Health    *Health   `yaml:"health"`
	Rollback  string    `yaml:"rollback"` // auto (default) | off
	OnFailure []Command `yaml:"on_failure"`

	// CloneURL overrides where the code is fetched from. It is normally derived
	// from the repository name and only needed for unusual setups.
	CloneURL string `yaml:"clone_url"`

	RunOnStart       bool  `yaml:"run_on_start"`
	AllowRepoScripts bool  `yaml:"allow_repo_scripts"`
	Enabled          *bool `yaml:"enabled"`
}

// Trigger describes what makes deckhand deploy.
type Trigger struct {
	Type       string `yaml:"type"`
	Branch     string `yaml:"branch"`
	TagMatch   string `yaml:"tag_match"`
	Prerelease bool   `yaml:"prerelease"`
}

// Verify holds optional supply-chain checks.
type Verify struct {
	RequireSignedTag    bool     `yaml:"require_signed_tag"`
	RequireSignedCommit bool     `yaml:"require_signed_commit"`
	AllowedSigners      string   `yaml:"allowed_signers"`
	PinSHA              string   `yaml:"pin_sha"`
	AllowedAuthors      []string `yaml:"allowed_authors"`
}

// Health is the post-deploy check.
type Health struct {
	HTTP         string   `yaml:"http"`
	ExpectStatus int      `yaml:"expect_status"`
	Cmd          *Command `yaml:"cmd"`
	Retries      int      `yaml:"retries"`
	Interval     Duration `yaml:"interval"`
	Timeout      Duration `yaml:"timeout"`
	InitialDelay Duration `yaml:"initial_delay"`
}

// Command is one thing to execute. In YAML it may be written either as a plain
// argv list:
//
//	run:
//	  - ["docker", "compose", "up", "-d"]
//
// or as a mapping when more control is needed:
//
//	run:
//	  - cmd: ["make", "deploy"]
//	    dir: build
//	    timeout: 20m
type Command struct {
	Cmd     []string          `yaml:"cmd"`
	Shell   string            `yaml:"shell"`
	Dir     string            `yaml:"dir"`
	Timeout Duration          `yaml:"timeout"`
	Env     map[string]string `yaml:"env"`
}

func (c *Command) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.SequenceNode:
		var argv []string
		if err := value.Decode(&argv); err != nil {
			return fmt.Errorf("command list must contain strings: %w", err)
		}
		c.Cmd = argv
		return nil
	case yaml.ScalarNode:
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		return fmt.Errorf("command %q is a plain string; write it as a list "+
			"([\"systemctl\", \"restart\", \"app\"]) or use \"shell:\" explicitly", s)
	case yaml.MappingNode:
		type raw Command
		var r raw
		if err := value.Decode(&r); err != nil {
			return err
		}
		*c = Command(r)
		if len(c.Cmd) == 0 && c.Shell == "" {
			return fmt.Errorf("command needs either \"cmd\" or \"shell\"")
		}
		if len(c.Cmd) > 0 && c.Shell != "" {
			return fmt.Errorf("command has both \"cmd\" and \"shell\"; pick one")
		}
		return nil
	}
	return fmt.Errorf("unsupported command syntax")
}

// String renders the command for logs.
func (c Command) String() string {
	if c.Shell != "" {
		return "sh -c " + c.Shell
	}
	return strings.Join(c.Cmd, " ")
}

// Load reads, validates and normalises a config file.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("cannot read config %s: %w", abs, err)
	}
	if err := checkPermissions(abs, info); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	cfg := &Config{Path: abs}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(abs), err)
	}
	if err := cfg.normalise(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// checkPermissions refuses group- or world-writable configs, and warns loudly
// about world-readable ones when they may contain an inline token.
func checkPermissions(path string, info os.FileInfo) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	mode := info.Mode().Perm()
	if mode&0o022 != 0 {
		return fmt.Errorf("config %s is writable by group or others (mode %04o); "+
			"run: chmod 600 %s", path, mode, path)
	}
	return nil
}

func (c *Config) normalise() error {
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version %d (this build understands version 1)", c.Version)
	}
	d := &c.Defaults
	if d.PollInterval == 0 {
		d.PollInterval = Duration(60 * time.Second)
	}
	if d.CommandTimeout == 0 {
		d.CommandTimeout = Duration(10 * time.Minute)
	}
	if d.Strategy == "" {
		d.Strategy = StrategyReleases
	}
	if d.KeepReleases == 0 {
		d.KeepReleases = 5
	}
	if d.FailureLimit == 0 {
		d.FailureLimit = 3
	}
	if c.GitHub.API == "" {
		c.GitHub.API = "https://api.github.com"
	}
	c.GitHub.API = strings.TrimRight(c.GitHub.API, "/")
	if c.GitHub.Host == "" {
		c.GitHub.Host = "github.com"
	}
	if c.Notify.Format == "" {
		c.Notify.Format = "json"
	}
	if len(c.Notify.On) == 0 {
		c.Notify.On = []string{"failure", "rollback"}
	}
	if len(c.Watches) == 0 {
		return fmt.Errorf("no watch entries configured")
	}

	seen := map[string]bool{}
	for i, w := range c.Watches {
		if w.Name == "" {
			return fmt.Errorf("watch #%d has no name", i+1)
		}
		if seen[w.Name] {
			return fmt.Errorf("duplicate watch name %q", w.Name)
		}
		seen[w.Name] = true
		if err := w.normalise(c); err != nil {
			return fmt.Errorf("watch %q: %w", w.Name, err)
		}
	}
	return nil
}

func (w *Watch) normalise(c *Config) error {
	if !strings.Contains(w.Repo, "/") || strings.Count(w.Repo, "/") != 1 {
		return fmt.Errorf("repo must be \"owner/name\", got %q", w.Repo)
	}
	if w.CloneURL == "" {
		w.CloneURL = fmt.Sprintf("https://%s/%s.git", c.GitHub.Host, w.Repo)
	}
	if w.Auth == "" {
		w.Auth = "token"
	}
	if w.Auth != "token" && w.Auth != "none" {
		return fmt.Errorf("auth must be \"token\" or \"none\"")
	}
	if w.Path == "" {
		return fmt.Errorf("path is required")
	}
	abs, err := filepath.Abs(os.ExpandEnv(w.Path))
	if err != nil {
		return err
	}
	w.Path = abs

	switch w.Trigger.Type {
	case TriggerRelease:
	case TriggerBranch:
		if w.Trigger.Branch == "" {
			return fmt.Errorf("trigger type \"branch\" needs a branch name")
		}
	case TriggerTag:
		if w.Trigger.TagMatch == "" {
			w.Trigger.TagMatch = "*"
		}
	case "":
		return fmt.Errorf("trigger type is required (release, branch or tag)")
	default:
		return fmt.Errorf("unknown trigger type %q", w.Trigger.Type)
	}

	if w.Strategy == "" {
		w.Strategy = c.Defaults.Strategy
	}
	if w.Strategy != StrategyReleases && w.Strategy != StrategyInplace {
		return fmt.Errorf("strategy must be %q or %q", StrategyReleases, StrategyInplace)
	}
	if w.Strategy == StrategyInplace && len(w.Shared) > 0 {
		return fmt.Errorf("shared paths require strategy %q", StrategyReleases)
	}
	if w.PollInterval == 0 {
		w.PollInterval = c.Defaults.PollInterval
	}
	if time.Duration(w.PollInterval) < 10*time.Second {
		return fmt.Errorf("poll_interval must be at least 10s")
	}
	if w.CommandTimeout == 0 {
		w.CommandTimeout = c.Defaults.CommandTimeout
	}
	if w.KeepReleases == 0 {
		w.KeepReleases = c.Defaults.KeepReleases
	}
	if w.FailureLimit == 0 {
		w.FailureLimit = c.Defaults.FailureLimit
	}
	if w.Rollback == "" {
		w.Rollback = "auto"
	}
	if w.Rollback != "auto" && w.Rollback != "off" {
		return fmt.Errorf("rollback must be \"auto\" or \"off\"")
	}
	if w.Rollback == "auto" && w.Strategy == StrategyInplace {
		w.Rollback = "auto" // supported: inplace rolls back by checking out the old SHA
	}
	if err := w.Window.Compile(c.Defaults.Timezone); err != nil {
		return err
	}
	if w.Health != nil {
		if w.Health.HTTP == "" && w.Health.Cmd == nil {
			return fmt.Errorf("health needs either \"http\" or \"cmd\"")
		}
		if w.Health.HTTP != "" && w.Health.Cmd != nil {
			return fmt.Errorf("health has both \"http\" and \"cmd\"; pick one")
		}
		if w.Health.Retries == 0 {
			w.Health.Retries = 5
		}
		if w.Health.Interval == 0 {
			w.Health.Interval = Duration(3 * time.Second)
		}
		if w.Health.Timeout == 0 {
			w.Health.Timeout = Duration(10 * time.Second)
		}
		if w.Health.ExpectStatus == 0 {
			w.Health.ExpectStatus = 200
		}
	}
	if len(w.Run) == 0 {
		return fmt.Errorf("no run commands configured; a watch without a command does nothing useful")
	}
	return nil
}

// IsEnabled reports whether the watch should be scheduled.
func (w *Watch) IsEnabled() bool { return w.Enabled == nil || *w.Enabled }

// Owner and Name split the repo field.
func (w *Watch) Owner() string    { o, _, _ := strings.Cut(w.Repo, "/"); return o }
func (w *Watch) RepoName() string { _, n, _ := strings.Cut(w.Repo, "/"); return n }

// StateDir is where deckhand keeps its bookkeeping for this watch.
func (w *Watch) StateDir(defaultDir string) string {
	if defaultDir != "" {
		return filepath.Join(defaultDir, w.Name)
	}
	return filepath.Join(w.Path, ".deckhand")
}

// Token resolves the GitHub token from the configured source. It returns an
// empty string when no token is configured, which is valid for public repos.
func (g GitHub) ResolveToken() (string, error) {
	if g.TokenFile != "" {
		info, err := os.Stat(g.TokenFile)
		if err != nil {
			return "", fmt.Errorf("token_file: %w", err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("token_file %s is readable by others (mode %04o); run: chmod 600 %s",
				g.TokenFile, info.Mode().Perm(), g.TokenFile)
		}
		b, err := os.ReadFile(g.TokenFile)
		if err != nil {
			return "", fmt.Errorf("token_file: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if g.TokenEnv != "" {
		return strings.TrimSpace(os.Getenv(g.TokenEnv)), nil
	}
	if g.Token != "" {
		return strings.TrimSpace(g.Token), nil
	}
	return strings.TrimSpace(os.Getenv("DECKHAND_GITHUB_TOKEN")), nil
}

// DefaultConfigPath returns the first sensible location for deckhand.yaml.
func DefaultConfigPath() string {
	if p := os.Getenv("DECKHAND_CONFIG"); p != "" {
		return p
	}
	candidates := []string{"deckhand.yaml", "deckhand.yml"}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "deckhand", "deckhand.yaml")
		}
		return "deckhand.yaml"
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".config", "deckhand", "deckhand.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/etc/deckhand/deckhand.yaml"
}
