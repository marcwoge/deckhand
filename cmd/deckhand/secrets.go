package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/marcwoge/deckhand/internal/config"
)

// cmdSecrets answers "which credential comes from where" without printing a
// single value. Today that question needs reading the config and stat-ing each
// file by hand, which is how a world-readable token file survives unnoticed.
func cmdSecrets(args []string) error {
	fs := flag.NewFlagSet("secrets", flag.ExitOnError)
	cfgPath := addConfigFlag(fs)
	asJSON := fs.Bool("json", false, "machine-readable output, for tooling")
	asTSV := fs.Bool("tsv", false, "tab-separated name/kind/path/mode, readable from a shell script")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}

	var rows []secretRow
	add := func(what string, spec config.SecretSpec) {
		if !spec.Set() {
			return
		}
		r := secretRow{What: what, Source: spec.Describe(), Note: note(spec), Kind: kind(spec)}
		if spec.File != "" {
			r.Path = os.ExpandEnv(spec.File)
			if info, err := os.Stat(r.Path); err == nil {
				r.Mode = fmt.Sprintf("%04o", info.Mode().Perm())
			}
		}
		r.Name = credentialName(what)
		rows = append(rows, r)
	}

	if cfg.GitHub.App != nil {
		add("github app key", cfg.GitHub.App.KeySpec())
	}
	add("github token", cfg.GitHub.TokenSpec())
	for _, w := range cfg.Watches {
		if w.Auth.Anonymous() {
			rows = append(rows, secretRow{What: "watch " + w.Name, Kind: "none",
				Source: "none", Note: "anonymous, public repositories only"})
			continue
		}
		if w.Auth.App != nil {
			add("watch "+w.Name+" app key", w.Auth.App.KeySpec())
			continue
		}
		add("watch "+w.Name, w.Auth.Spec())
	}
	for host, auth := range cfg.Registry {
		add("registry "+host, auth.Spec())
	}
	for i, ch := range cfg.Notify.Channels {
		add(fmt.Sprintf("notify #%d (%s)", i+1, ch.Type), ch.Spec())
	}

	if *asTSV {
		// Tab-separated so scripts/encrypt-credentials.sh needs neither jq nor
		// python to read it.
		//
		// CodeQL's go/clear-text-logging flags the path here, because it reads
		// it from a field named PasswordFile and treats anything so named as a
		// password. It is a path, not a credential, and naming the file is the
		// point of this command - the operator has to know which file to check.
		// TestSecretsOutputNeverContainsAValue pins that down. Renaming the
		// field would silence the query and lose something real: the name is
		// what keeps it watching the field that does hold the value.
		for _, r := range rows {
			fmt.Printf("%s\t%s\t%s\t%s\n", r.Name, r.Kind, r.Path, r.Mode)
		}
		return nil
	}
	if *asJSON {
		// Scripts - scripts/encrypt-credentials.sh among them - need the list
		// without parsing a table.
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if rows == nil {
			rows = []secretRow{}
		}
		return enc.Encode(rows)
	}

	if len(rows) == 0 {
		fmt.Println("no credentials configured (public repositories only)")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CREDENTIAL\tSOURCE\tNOTE")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.What, r.Source, r.Note)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Println("\nNo values are printed. See docs/en/security.md for encrypting credentials at rest.")
	return nil
}

// secretRow is one credential in the overview. The JSON form is a documented
// interface: scripts/encrypt-credentials.sh reads it.
type secretRow struct {
	// What names the setting, e.g. "watch shop".
	What string `json:"what"`
	// Name is the filename-safe credential name, matching what
	// "service install" writes into the systemd unit.
	Name   string `json:"name"`
	Kind   string `json:"kind"` // file | env | inline | command | none
	Source string `json:"source"`
	// Path is set for a file credential, with the environment expanded.
	Path string `json:"path,omitempty"`
	Mode string `json:"mode,omitempty"`
	Note string `json:"note"`
}

// kind reduces a source to one word, so a script can branch on it.
func kind(spec config.SecretSpec) string {
	switch {
	case spec.Command != nil:
		return "command"
	case spec.File != "":
		return "file"
	case spec.Env != "":
		return "env"
	case spec.Inline != "":
		return "inline"
	}
	return "none"
}

// credentialName turns a description into the same filename-safe name that the
// generated systemd unit uses, so the two can be matched up.
func credentialName(what string) string {
	name := strings.NewReplacer(" ", "-", "#", "", "(", "", ")", "", ".", "-").Replace(what)
	switch {
	case name == "github-token", name == "github-app-key":
		return name
	case strings.HasPrefix(name, "watch-"):
		if strings.HasSuffix(name, "-app-key") {
			return name
		}
		return name + "-token"
	case strings.HasPrefix(name, "registry-"):
		return name + "-password"
	case strings.HasPrefix(name, "notify-"):
		// "notify-1-ntfy" -> "notify-1-token", matching credentialFiles.
		parts := strings.Split(name, "-")
		if len(parts) >= 2 {
			return "notify-" + parts[1] + "-token"
		}
	}
	return name
}

// note reports what is worth knowing about one source: bad permissions, or the
// fact that an environment variable is not more private than a file.
func note(spec config.SecretSpec) string {
	switch {
	case spec.Command != nil:
		return "fetched from a secret manager"
	case spec.File != "":
		path := os.ExpandEnv(spec.File)
		info, err := os.Stat(path)
		if err != nil {
			return "unreadable: " + err.Error()
		}
		perm := info.Mode().Perm()
		if runtime.GOOS != "windows" && perm&0o077 != 0 {
			return fmt.Sprintf("mode %04o - readable by others, run: chmod 600 %s", perm, path)
		}
		return fmt.Sprintf("mode %04o", perm)
	case spec.Env != "":
		// Not a hole - only root and the same user can read it - but operators
		// assume an environment variable is more private than a file, and it is
		// not.
		return "visible in /proc/<pid>/environ to root and this user"
	}
	return "inline in the config file; prefer a file or a command"
}

// credentialFiles collects the credential files a configuration reads, as
// name -> path, for the systemd-creds hint in the generated unit. Names are the
// credential names systemd will use, so they must be filename-safe.
func credentialFiles(cfg *config.Config) map[string]string {
	files := map[string]string{}
	add := func(name string, spec config.SecretSpec) {
		if spec.File != "" {
			files[name] = os.ExpandEnv(spec.File)
		}
	}
	add("github-token", cfg.GitHub.TokenSpec())
	if cfg.GitHub.App != nil {
		add("github-app-key", cfg.GitHub.App.KeySpec())
	}
	for _, w := range cfg.Watches {
		if w.Auth.App != nil {
			add("watch-"+w.Name+"-app-key", w.Auth.App.KeySpec())
			continue
		}
		add("watch-"+w.Name+"-token", w.Auth.Spec())
	}
	for i, ch := range cfg.Notify.Channels {
		add(fmt.Sprintf("notify-%d-token", i+1), ch.Spec())
	}
	for host, auth := range cfg.Registry {
		add("registry-"+strings.ReplaceAll(host, ".", "-")+"-password", auth.Spec())
	}
	return files
}
