// Package config loads and validates deckhand.yaml.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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
	TriggerImage   = "image"
)

// Config is the whole deckhand.yaml file.
type Config struct {
	Version  int      `yaml:"version"`
	Defaults Defaults `yaml:"defaults"`
	GitHub   GitHub   `yaml:"github"`
	// Registry holds credentials per registry host, keyed by host name.
	Registry  map[string]*RegistryAuth `yaml:"registry"`
	Notify    Notify                   `yaml:"notify"`
	Heartbeat Heartbeat                `yaml:"heartbeat"`
	Watches   []*Watch                 `yaml:"watch"`

	// Include names a directory of additional *.yaml files, each of which may
	// contribute watch entries. Defaults to "<config>.d" when that exists.
	Include string `yaml:"include"`

	// Path is the file this config was read from (not part of the YAML).
	Path string `yaml:"-"`
	// Sources lists every file that contributed to this config.
	Sources []string `yaml:"-"`
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
	Audit          Audit             `yaml:"audit"`
}

// Audit controls how much history is kept on disk.
type Audit struct {
	MaxSize Size `yaml:"max_size"`
	Keep    int  `yaml:"keep"`
}

// GitHub holds API access settings. Prefer TokenEnv or TokenFile over Token.
type GitHub struct {
	Token     string `yaml:"token"`
	TokenEnv  string `yaml:"token_env"`
	TokenFile string `yaml:"token_file"`
	// TokenCommand reads the token from a secret manager: pass, Vault, the
	// 1Password CLI, sops, aws secretsmanager, a company wrapper script.
	TokenCommand *Command `yaml:"token_command"`
	// TokenTTL is how long a token from TokenCommand is reused before the
	// command runs again. Default 1h.
	TokenTTL Duration `yaml:"token_ttl"`
	API      string   `yaml:"api"`
	// Host is the git host used for cloning; change it for GitHub Enterprise.
	Host string `yaml:"host"`

	// App authenticates as a GitHub App instead of with a personal token.
	App *GitHubApp `yaml:"app"`
}

// GitHubApp authenticates as a GitHub App. Its private key mints installation
// tokens that last an hour and renew themselves, so nothing expires the way a
// personal access token does - and the identity is the app, not a person.
type GitHubApp struct {
	// ID is the app id (the numeric one, or its client id).
	ID string `yaml:"id"`

	// InstallationID pins every repository to one installation. Leave it unset
	// to have the installation discovered per repository, which is what you
	// want when the app is installed in more than one account.
	InstallationID int64 `yaml:"installation_id"`

	PrivateKey        string   `yaml:"private_key"`
	PrivateKeyFile    string   `yaml:"private_key_file"`
	PrivateKeyEnv     string   `yaml:"private_key_env"`
	PrivateKeyCommand *Command `yaml:"private_key_command"`
}

// KeySpec names where the private key comes from.
func (a *GitHubApp) KeySpec() SecretSpec {
	return SecretSpec{What: "github.app private_key", Inline: a.PrivateKey,
		Env: a.PrivateKeyEnv, File: a.PrivateKeyFile, Command: a.PrivateKeyCommand}
}

// ResolvePrivateKey reads the PEM key from its configured source, refusing one
// that others can read.
func (a *GitHubApp) ResolvePrivateKey() ([]byte, error) {
	key, err := a.KeySpec().Resolve()
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, fmt.Errorf("no private key configured")
	}
	// A key pasted into YAML or an environment variable usually loses its
	// newlines; restore them so the PEM decoder can read it.
	return []byte(strings.ReplaceAll(key, "\\n", "\n")), nil
}

// Notify configures outbound notifications.
//
// A single unauthenticated destination can be written in the short form
// (webhook + format). Anything else - several destinations, a token, Telegram -
// uses the channels list.
type Notify struct {
	On []string `yaml:"on"` // success, failure, rollback, halt

	// Short form for one destination.
	Webhook string `yaml:"webhook"`
	Format  string `yaml:"format"` // json (default), slack, ntfy

	Channels []*Channel `yaml:"channels"`
}

// Channel is one notification destination.
type Channel struct {
	Type string `yaml:"type"` // ntfy | slack | webhook | telegram
	URL  string `yaml:"url"`

	// Credentials. Prefer token_env or token_file over an inline token.
	Token        string   `yaml:"token"`
	TokenEnv     string   `yaml:"token_env"`
	TokenFile    string   `yaml:"token_file"`
	TokenCommand *Command `yaml:"token_command"`

	// Telegram only.
	ChatID   string `yaml:"chat_id"`
	Commands bool   `yaml:"commands"`

	// On overrides notify.on for this channel, so you can send everything to
	// one place and only failures to another.
	On []string `yaml:"on"`

	// Priority maps an event to an ntfy priority (min, low, default, high,
	// urgent). Unset events use a sensible default.
	Priority map[string]string `yaml:"priority"`
}

// Heartbeat pings a dead-man's-switch service so an outage of deckhand itself
// is noticed. Silence from a crashed worker otherwise looks exactly like
// silence from a healthy one.
type Heartbeat struct {
	URL      string   `yaml:"url"`
	Interval Duration `yaml:"interval"`
	Method   string   `yaml:"method"`
	Timeout  Duration `yaml:"timeout"`
}

// Watch is a single repository being observed.
type Watch struct {
	Name string `yaml:"name"`
	Repo string `yaml:"repo"` // owner/name
	// Auth is "token" (the default, using the global credential), "none", or a
	// block naming a credential for this repository alone.
	Auth    WatchAuth `yaml:"auth"`
	Trigger Trigger   `yaml:"trigger"`
	Path    string    `yaml:"path"`

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

	// Image and Tag apply to the image trigger: the container image to watch,
	// and which tag of it. A new digest under the same tag counts as a change,
	// which is what a rebuilt "latest" is.
	Image string `yaml:"image"`
	Tag   string `yaml:"tag"`
}

// WatchAuth is the credential one watch uses. It exists because one token for
// every repository is both wrong (repositories live in different accounts) and
// risky (a single token that reads everything is worth more to an attacker
// than one token per repository).
//
// It accepts the two short forms it always did - "auth: token" and
// "auth: none" - or a block naming a credential for this watch alone.
type WatchAuth struct {
	// Mode is "token" (use a credential) or "none" (anonymous). In the block
	// form it can be written out, so that "mode: none" next to a token is
	// caught as the contradiction it is rather than silently ignored.
	Mode string `yaml:"mode"`

	// A credential for this watch only. Empty means the global one.
	Token        string   `yaml:"token"`
	TokenEnv     string   `yaml:"token_env"`
	TokenFile    string   `yaml:"token_file"`
	TokenCommand *Command `yaml:"token_command"`
	TokenTTL     Duration `yaml:"token_ttl"`

	// App is a GitHub App installation for this watch only, for a machine
	// watching repositories covered by different apps.
	App *GitHubApp `yaml:"app"`
}

// UnmarshalYAML accepts both the string form and the block form.
func (a *WatchAuth) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var mode string
		if err := value.Decode(&mode); err != nil {
			return err
		}
		a.Mode = mode
		return nil
	case yaml.MappingNode:
		type raw WatchAuth
		var r raw
		if err := value.Decode(&r); err != nil {
			return err
		}
		*a = WatchAuth(r)
		if a.Mode == "" {
			a.Mode = "token"
		}
		return nil
	}
	return fmt.Errorf("auth must be \"token\", \"none\", or a block naming a credential")
}

// Anonymous reports whether this watch is fetched without a credential.
func (a WatchAuth) Anonymous() bool { return a.Mode == "none" }

// Own reports whether this watch brings its own credential rather than using
// the global one.
func (a WatchAuth) Own() bool { return a.Spec().Set() || a.App != nil }

// Describe names the credential source, for "check" and "doctor". It never
// prints a value.
func (a WatchAuth) Describe() string {
	switch {
	case a.Anonymous():
		return "none"
	case a.App != nil:
		return "app " + a.App.ID
	case a.Spec().Set():
		return a.Spec().Describe()
	}
	return "token (global)"
}

// ResolveToken reads this watch's own credential. It is only called when Own
// reports true.
func (a WatchAuth) ResolveToken() (string, error) {
	return a.Spec().Resolve()
}

// validate checks one watch credential.
func (a *WatchAuth) validate() error {
	if a.Mode == "" {
		a.Mode = "token"
	}
	if a.Mode != "token" && a.Mode != "none" {
		return fmt.Errorf("auth must be \"token\" or \"none\", got %q", a.Mode)
	}
	if a.Mode == "none" && a.Own() {
		return fmt.Errorf("auth is \"none\" and also names a credential; pick one")
	}
	if a.Spec().Set() && a.App != nil {
		return fmt.Errorf("auth has both a token and an app; pick one")
	}
	if err := a.Spec().validate(); err != nil {
		return err
	}
	if a.App != nil {
		if a.App.ID == "" {
			return fmt.Errorf("auth.app needs an id")
		}
		if err := a.App.KeySpec().validate(); err != nil {
			return err
		}
		if !a.App.KeySpec().FromCommand() {
			if _, err := a.App.ResolvePrivateKey(); err != nil {
				return fmt.Errorf("auth.app: %w", err)
			}
		}
		if a.App.InstallationID < 0 {
			return fmt.Errorf("auth.app installation_id cannot be negative")
		}
	}
	// A credential from a command cannot be checked here - running a
	// subprocess during parsing is the wrong place, and internal/secret
	// cannot be imported from this package. It is executed at startup
	// instead, which is early enough to fail before any deployment.
	if !a.Spec().FromCommand() && a.Spec().Set() {
		// A token file others can read is refused here, exactly as the global
		// one is.
		if _, err := a.ResolveToken(); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	return nil
}

// RegistryAuth is the pull credential for one registry host.
type RegistryAuth struct {
	Username        string   `yaml:"username"`
	Password        string   `yaml:"password"`
	PasswordEnv     string   `yaml:"password_env"`
	PasswordFile    string   `yaml:"password_file"`
	PasswordCommand *Command `yaml:"password_command"`
}

// Spec names where the registry password comes from.
func (r *RegistryAuth) Spec() SecretSpec {
	return SecretSpec{What: "registry password", Inline: r.Password,
		Env: r.PasswordEnv, File: r.PasswordFile, Command: r.PasswordCommand}
}

// ResolvePassword reads the registry credential from its configured source.
func (r *RegistryAuth) ResolvePassword() (string, error) {
	return r.Spec().Resolve()
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

// Label names a command without repeating its arguments. It exists for
// credential commands: an argument can itself contain the credential (think
// sh -c "echo $TOKEN"), so only the program is safe to print in a log or an
// error message.
func (c Command) Label() string {
	if c.Shell != "" {
		return "shell credential command"
	}
	if len(c.Cmd) > 0 {
		return filepath.Base(c.Cmd[0])
	}
	return "credential command"
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
	cfg := &Config{Path: abs, Sources: []string{abs}}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(abs), err)
	}
	if err := cfg.loadIncludes(); err != nil {
		return nil, err
	}
	if err := cfg.normalise(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// includeFile is the subset of the config an included file may set. Defaults,
// credentials and notifications stay in the main file, so there is never a
// question about which one wins.
type includeFile struct {
	Watches []*Watch `yaml:"watch"`
}

// loadIncludes reads watch entries from the include directory, in lexical
// order so the result does not depend on the filesystem.
func (c *Config) loadIncludes() error {
	dir := c.Include
	if dir == "" {
		// Convention over configuration: deckhand.yaml picks up deckhand.yaml.d
		// automatically when it is there.
		candidate := c.Path + ".d"
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			dir = candidate
		} else {
			return nil
		}
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(c.Path), dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("include directory %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := strings.ToLower(filepath.Ext(e.Name())); ext != ".yaml" && ext != ".yml" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		// Included files decide what runs just as much as the main file does.
		if err := checkPermissions(path, info); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var inc includeFile
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		if err := dec.Decode(&inc); err != nil {
			return fmt.Errorf("%s: %w (included files may only contain watch entries)", name, err)
		}
		c.Watches = append(c.Watches, inc.Watches...)
		c.Sources = append(c.Sources, path)
	}
	return nil
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
	if d.Audit.MaxSize == 0 {
		d.Audit.MaxSize = Size(10 << 20)
	}
	if d.Audit.Keep == 0 {
		d.Audit.Keep = 5
	}
	if d.Audit.Keep < 0 {
		return fmt.Errorf("audit.keep cannot be negative")
	}
	if c.GitHub.API == "" {
		c.GitHub.API = "https://api.github.com"
	}
	c.GitHub.API = strings.TrimRight(c.GitHub.API, "/")
	if c.GitHub.Host == "" {
		c.GitHub.Host = "github.com"
	}
	if app := c.GitHub.App; app != nil {
		if strings.TrimSpace(app.ID) == "" {
			return fmt.Errorf("github.app needs an id (Settings -> Developer settings -> GitHub Apps)")
		}
		sources := 0
		for _, set := range []string{app.PrivateKey, app.PrivateKeyFile, app.PrivateKeyEnv} {
			if set != "" {
				sources++
			}
		}
		if app.PrivateKeyCommand != nil {
			sources++
		}
		switch sources {
		case 0:
			return fmt.Errorf("github.app needs private_key_file (preferred), " +
				"private_key_command, private_key_env or private_key")
		case 1:
		default:
			return fmt.Errorf("github.app has several private key sources; pick one")
		}
		if !app.KeySpec().FromCommand() {
			if _, err := app.ResolvePrivateKey(); err != nil {
				return fmt.Errorf("github.app: %w", err)
			}
		}
		if c.GitHub.TokenSpec().Set() {
			return fmt.Errorf("github has both a personal token and an app configured; pick one")
		}
		if app.InstallationID < 0 {
			return fmt.Errorf("github.app installation_id cannot be negative")
		}
	}
	if err := c.GitHub.TokenSpec().validate(); err != nil {
		return err
	}
	if c.Notify.Format == "" {
		c.Notify.Format = "json"
	}
	if len(c.Notify.On) == 0 {
		c.Notify.On = []string{"failure", "rollback", "halt"}
	}
	if err := c.Notify.normalise(); err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	if err := c.Heartbeat.normalise(); err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	for host, auth := range c.Registry {
		if host == "" {
			return fmt.Errorf("registry entry without a host name")
		}
		if err := auth.Spec().validate(); err != nil {
			return fmt.Errorf("registry %s: %w", host, err)
		}
		if !auth.Spec().FromCommand() {
			if _, err := auth.ResolvePassword(); err != nil {
				return fmt.Errorf("registry %s: %w", host, err)
			}
		}
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
	// An image trigger watches a registry, so there is no repository involved.
	if w.Trigger.Type == TriggerImage {
		if w.Repo != "" {
			return fmt.Errorf("an image trigger watches a registry; remove \"repo\"")
		}
		if w.Trigger.Image == "" {
			return fmt.Errorf("trigger type \"image\" needs an image, e.g. ghcr.io/you/app")
		}
		if strings.Contains(w.Trigger.Image, "@") {
			return fmt.Errorf("image %q already pins a digest; give a tag and deckhand "+
				"resolves the digest itself", w.Trigger.Image)
		}
		if w.Trigger.Tag == "" && w.Trigger.TagMatch == "" {
			w.Trigger.Tag = "latest"
		}
		if w.Trigger.Tag != "" && w.Trigger.TagMatch != "" {
			return fmt.Errorf("an image trigger takes either \"tag\" or \"tag_match\", not both")
		}
		if w.Trigger.Branch != "" {
			return fmt.Errorf("an image trigger has no branch")
		}
	} else if !strings.Contains(w.Repo, "/") || strings.Count(w.Repo, "/") != 1 {
		return fmt.Errorf("repo must be \"owner/name\", got %q", w.Repo)
	}
	if w.CloneURL == "" && w.Repo != "" {
		w.CloneURL = fmt.Sprintf("https://%s/%s.git", c.GitHub.Host, w.Repo)
	}
	if err := w.Auth.validate(); err != nil {
		return err
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
	case TriggerImage:
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
		return fmt.Errorf("trigger type is required (release, branch, tag or image)")
	default:
		return fmt.Errorf("unknown trigger type %q", w.Trigger.Type)
	}

	if w.Trigger.Type == TriggerImage {
		// Nothing is checked out, so the release/in-place distinction and shared
		// paths have nothing to act on.
		if len(w.Shared) > 0 {
			return fmt.Errorf("shared paths need a checkout; an image trigger has none")
		}
		if w.Strategy != "" {
			return fmt.Errorf("strategy applies to a checkout; an image trigger has none")
		}
		w.Strategy = StrategyInplace
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

func (n *Notify) normalise() error {
	// The short form becomes an ordinary channel, so everything downstream
	// only ever deals with the list.
	if n.Webhook != "" {
		n.Channels = append([]*Channel{{
			Type: n.Format,
			URL:  n.Webhook,
		}}, n.Channels...)
		n.Webhook = ""
	}
	seenCommands := false
	for i, ch := range n.Channels {
		if ch.Type == "" {
			return fmt.Errorf("channel #%d has no type (ntfy, slack, webhook or telegram)", i+1)
		}
		switch ch.Type {
		case "ntfy", "slack", "webhook", "json":
			if ch.Type == "json" {
				ch.Type = "webhook"
			}
			if ch.URL == "" {
				return fmt.Errorf("channel #%d (%s) needs a url", i+1, ch.Type)
			}
			if !strings.HasPrefix(ch.URL, "http://") && !strings.HasPrefix(ch.URL, "https://") {
				return fmt.Errorf("channel #%d: url must start with http:// or https://", i+1)
			}
			if ch.ChatID != "" || ch.Commands {
				return fmt.Errorf("channel #%d: chat_id and commands only apply to telegram", i+1)
			}
		case "telegram":
			if ch.ChatID == "" {
				return fmt.Errorf("channel #%d (telegram) needs a chat_id; message your "+
					"bot once, then read it from "+
					"https://api.telegram.org/bot<TOKEN>/getUpdates", i+1)
			}
			if ch.URL != "" {
				return fmt.Errorf("channel #%d: telegram takes no url", i+1)
			}
			if ch.Commands {
				if seenCommands {
					return fmt.Errorf("only one telegram channel may enable commands")
				}
				seenCommands = true
			}
		default:
			return fmt.Errorf("channel #%d: unknown type %q", i+1, ch.Type)
		}
		if err := ch.Spec().validate(); err != nil {
			return fmt.Errorf("channel #%d: %w", i+1, err)
		}
		if !ch.Spec().FromCommand() {
			if _, err := ch.ResolveToken(); err != nil {
				return fmt.Errorf("channel #%d: %w", i+1, err)
			}
			if ch.Type == "telegram" {
				if tok, _ := ch.ResolveToken(); tok == "" {
					return fmt.Errorf("channel #%d (telegram) needs a bot token", i+1)
				}
			}
		}
		for event, prio := range ch.Priority {
			switch prio {
			case "min", "low", "default", "high", "urgent":
			default:
				return fmt.Errorf("channel #%d: priority for %q must be min, low, default, high or urgent",
					i+1, event)
			}
		}
	}
	return nil
}

// CommandChannel returns the telegram channel that accepts commands, if any.
func (n *Notify) CommandChannel() *Channel {
	for _, ch := range n.Channels {
		if ch.Type == "telegram" && ch.Commands {
			return ch
		}
	}
	return nil
}

// ResolveToken reads the channel credential from its configured source.
func (c *Channel) ResolveToken() (string, error) {
	return c.Spec().Resolve()
}

// Events returns the event list this channel reacts to, falling back to the
// global list.
func (c *Channel) Events(global []string) []string {
	if len(c.On) > 0 {
		return c.On
	}
	return global
}

func (h *Heartbeat) normalise() error {
	if h.URL == "" {
		return nil
	}
	if !strings.HasPrefix(h.URL, "http://") && !strings.HasPrefix(h.URL, "https://") {
		return fmt.Errorf("url must start with http:// or https://")
	}
	if h.Interval == 0 {
		h.Interval = Duration(5 * time.Minute)
	}
	if time.Duration(h.Interval) < 30*time.Second {
		return fmt.Errorf("interval must be at least 30s")
	}
	if h.Timeout == 0 {
		h.Timeout = Duration(15 * time.Second)
	}
	switch strings.ToUpper(h.Method) {
	case "":
		h.Method = "GET"
	case "GET", "POST", "HEAD":
		h.Method = strings.ToUpper(h.Method)
	default:
		return fmt.Errorf("method must be GET, POST or HEAD")
	}
	return nil
}

// Enabled reports whether a heartbeat is configured.
func (h Heartbeat) Enabled() bool { return h.URL != "" }

// NeedsCheckout reports whether this watch places a source tree on disk. An
// image trigger does not: the artefact is the image, and the command only has
// to restart the service.
func (w *Watch) NeedsCheckout() bool { return w.Trigger.Type != TriggerImage }

// Subject names what the watch follows, for logs and status output.
func (w *Watch) Subject() string {
	if w.Trigger.Type == TriggerImage {
		return w.Trigger.Image
	}
	return w.Repo
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
	secret, err := g.TokenSpec().Resolve()
	if err != nil {
		return "", err
	}
	if secret == "" {
		return strings.TrimSpace(os.Getenv("DECKHAND_GITHUB_TOKEN")), nil
	}
	return secret, nil
}

// SecretSpec names where one credential comes from. It exists so that every
// credential - GitHub token, app key, registry password, bot token - offers the
// same four sources without repeating the logic four times.
//
// The command is not executed here: running a subprocess belongs in
// internal/secret, which cannot be imported from this package without a cycle.
type SecretSpec struct {
	// What names the setting in error messages, e.g. "github.token".
	What    string
	Inline  string
	Env     string
	File    string
	Command *Command
	// TTL is how long a value from Command is reused. Zero means the default.
	TTL time.Duration
}

// FromCommand reports whether this credential comes from a command.
func (s SecretSpec) FromCommand() bool { return s.Command != nil }

// Set reports whether any source is configured at all.
func (s SecretSpec) Set() bool {
	return s.Inline != "" || s.Env != "" || s.File != "" || s.Command != nil
}

// Describe names the source without ever printing the value.
func (s SecretSpec) Describe() string {
	switch {
	case s.Command != nil:
		return "command " + s.Command.Label()
	case s.File != "":
		return "file " + os.ExpandEnv(s.File)
	case s.Env != "":
		return "environment " + s.Env
	case s.Inline != "":
		return "inline in the config file"
	}
	return "not configured"
}

// Resolve reads the credential from a file, an environment variable or an
// inline value. A command is not run here - callers that support one use
// internal/secret.
func (s SecretSpec) Resolve() (string, error) {
	return resolveSecretNamed(s.What, s.Inline, s.Env, s.File)
}

// validate checks the shape of a credential: one source, and a usable command.
func (s SecretSpec) validate() error {
	sources := 0
	for _, set := range []bool{s.Inline != "", s.Env != "", s.File != "", s.Command != nil} {
		if set {
			sources++
		}
	}
	if sources > 1 {
		return fmt.Errorf("%s has several sources; pick one", s.What)
	}
	if s.Command != nil {
		if len(s.Command.Cmd) == 0 && s.Command.Shell == "" {
			return fmt.Errorf("%s_command needs either a command list or \"shell\"", s.What)
		}
	}
	if s.TTL < 0 {
		return fmt.Errorf("%s ttl cannot be negative", s.What)
	}
	return nil
}

// TokenSpec names where the GitHub token comes from.
func (g GitHub) TokenSpec() SecretSpec {
	return SecretSpec{What: "github.token", Inline: g.Token, Env: g.TokenEnv,
		File: g.TokenFile, Command: g.TokenCommand, TTL: time.Duration(g.TokenTTL)}
}

// Spec names where this watch's own credential comes from.
func (a WatchAuth) Spec() SecretSpec {
	return SecretSpec{What: "auth.token", Inline: a.Token, Env: a.TokenEnv,
		File: a.TokenFile, Command: a.TokenCommand, TTL: time.Duration(a.TokenTTL)}
}

// Spec names where the channel token comes from.
func (c *Channel) Spec() SecretSpec {
	return SecretSpec{What: "channel token", Inline: c.Token, Env: c.TokenEnv,
		File: c.TokenFile, Command: c.TokenCommand}
}

// resolveSecret reads a credential from a file, an environment variable or an
// inline value, in that order of preference. A file that others can read is
// refused rather than used.
func resolveSecret(inline, env, file string) (string, error) {
	return resolveSecretNamed("token", inline, env, file)
}

func resolveSecretNamed(what, inline, env, file string) (string, error) {
	if file != "" {
		// Expanding the environment makes systemd's
		// ${CREDENTIALS_DIRECTORY} usable, which is how a credential can be
		// encrypted at rest and never touch persistent storage in plaintext.
		file = os.ExpandEnv(file)
		info, err := os.Stat(file)
		if err != nil {
			return "", fmt.Errorf("%s_file: %w", what, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf("%s_file %s is readable by others (mode %04o); run: chmod 600 %s",
				what, file, info.Mode().Perm(), file)
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("%s_file: %w", what, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if env != "" {
		return strings.TrimSpace(os.Getenv(env)), nil
	}
	return strings.TrimSpace(inline), nil
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
