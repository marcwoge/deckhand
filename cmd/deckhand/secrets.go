package main

import (
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
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}

	type row struct {
		what, source, note string
	}
	var rows []row
	add := func(what string, spec config.SecretSpec) {
		if !spec.Set() {
			return
		}
		rows = append(rows, row{what, spec.Describe(), note(spec)})
	}

	if cfg.GitHub.App != nil {
		add("github app key", cfg.GitHub.App.KeySpec())
	}
	add("github token", cfg.GitHub.TokenSpec())
	for _, w := range cfg.Watches {
		if w.Auth.Anonymous() {
			rows = append(rows, row{"watch " + w.Name, "none", "anonymous, public repositories only"})
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

	if len(rows) == 0 {
		fmt.Println("no credentials configured (public repositories only)")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CREDENTIAL\tSOURCE\tNOTE")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.what, r.source, r.note)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Println("\nNo values are printed. See docs/en/security.md for encrypting credentials at rest.")
	return nil
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
