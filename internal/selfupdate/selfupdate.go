// Package selfupdate replaces the running binary with a newer signed release.
//
// Deckhand keeps everything else current but was updated by hand, which means
// security fixes land late - the worst kind of late.
//
// A deployment worker that updates itself is a supply-chain path to every
// machine it runs on, so verification is not optional here and there is no flag
// to skip it: the checksum file is checked against the cosign signature bound to
// the release workflow, and only then is the asset's checksum compared. Checking
// SHA256SUMS alone would prove nothing, because it comes from the same place as
// the binary.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultRepo is the repository releases are taken from.
const DefaultRepo = "marcwoge/deckhand"

// identityPattern binds the signature to the release workflow of the repository
// the update comes from. A signature made by anything else is not accepted.
const identityPattern = "https://github.com/%s/.github/workflows/release.yml@.*"

const oidcIssuer = "https://token.actions.githubusercontent.com"

// maxAsset bounds a download. A deckhand binary is a few megabytes; anything
// near this is not one.
const maxAsset = 200 << 20

// Updater performs one update. The URLs and the verifier are fields so the
// whole thing can be driven against a local server in tests.
type Updater struct {
	Repo string
	// API and Download default to GitHub. Tests point them at a stub.
	API      string
	Download string
	// Current is the version of the running binary, as "v0.1.1" or "dev".
	Current string
	// Dest is the file to replace. Empty means the running executable.
	Dest string
	HTTP *http.Client
	// Verify checks the signature over the checksum file. Empty means cosign.
	Verify func(ctx context.Context, sums, bundle, repo string) error
	Logf   func(format string, args ...interface{})
}

// Result describes what an update did.
type Result struct {
	From string
	To   string
	// Previous is where the replaced binary was kept, if there was one.
	Previous string
	Dest     string
}

func (u *Updater) prepare() error {
	if u.Repo == "" {
		u.Repo = DefaultRepo
	}
	if u.API == "" {
		u.API = "https://api.github.com"
	}
	if u.Download == "" {
		u.Download = "https://github.com/" + u.Repo + "/releases/download"
	}
	if u.HTTP == nil {
		u.HTTP = &http.Client{Timeout: 5 * time.Minute}
	}
	if u.Verify == nil {
		u.Verify = verifyWithCosign
	}
	if u.Logf == nil {
		u.Logf = func(string, ...interface{}) {}
	}
	if u.Dest == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("cannot tell which binary is running: %w", err)
		}
		// Resolve the symlink so an update replaces the real file rather than
		// turning a link into a binary.
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		u.Dest = exe
	}
	return nil
}

// Latest returns the newest non-prerelease tag.
func (u *Updater) Latest(ctx context.Context) (string, error) {
	if err := u.prepare(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/releases/latest", u.API, u.Repo), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach %s: %w", u.API, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: unexpected status %d", u.API, resp.StatusCode)
	}
	var release struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return "", fmt.Errorf("unreadable release information: %w", err)
	}
	if release.Tag == "" {
		return "", fmt.Errorf("no release found in %s", u.Repo)
	}
	return release.Tag, nil
}

// AssetName is the release asset for this machine.
func AssetName(tag string) string {
	name := fmt.Sprintf("deckhand_%s_%s_%s", tag, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// Update downloads, verifies and installs one release.
func (u *Updater) Update(ctx context.Context, tag string) (Result, error) {
	if err := u.prepare(); err != nil {
		return Result{}, err
	}
	dir, err := os.MkdirTemp(filepath.Dir(u.Dest), ".deckhand-update-")
	if err != nil {
		// A temporary directory next to the destination keeps the install a
		// rename on the same filesystem. Fall back to the usual place.
		dir, err = os.MkdirTemp("", "deckhand-update-")
		if err != nil {
			return Result{}, err
		}
	}
	defer os.RemoveAll(dir)

	asset := AssetName(tag)
	u.Logf("downloading %s", asset)
	binary := filepath.Join(dir, asset)
	if err := u.fetch(ctx, tag, asset, binary); err != nil {
		return Result{}, err
	}
	sums := filepath.Join(dir, "SHA256SUMS")
	if err := u.fetch(ctx, tag, "SHA256SUMS", sums); err != nil {
		return Result{}, err
	}
	bundle := filepath.Join(dir, "SHA256SUMS.bundle")
	if err := u.fetch(ctx, tag, "SHA256SUMS.bundle", bundle); err != nil {
		return Result{}, fmt.Errorf("%s is not signed, refusing to install it: %w", tag, err)
	}

	u.Logf("verifying the signature")
	if err := u.Verify(ctx, sums, bundle, u.Repo); err != nil {
		return Result{}, err
	}

	u.Logf("verifying the checksum")
	want, err := checksumFor(sums, asset)
	if err != nil {
		return Result{}, err
	}
	got, err := sha256File(binary)
	if err != nil {
		return Result{}, err
	}
	if want != got {
		return Result{}, fmt.Errorf("checksum mismatch for %s: the release lists %s, "+
			"the download is %s", asset, want, got)
	}

	// A binary that cannot even state its version must not replace a working
	// one - wrong architecture, truncated download, a libc that is too old.
	if err := os.Chmod(binary, 0o755); err != nil {
		return Result{}, err
	}
	if out, err := exec.CommandContext(ctx, binary, "version").CombinedOutput(); err != nil {
		return Result{}, fmt.Errorf("the downloaded binary does not run on this machine: %w: %s",
			err, strings.TrimSpace(string(out)))
	}

	return u.install(binary, tag)
}

// install puts the new binary in place, keeping the old one next to it.
func (u *Updater) install(binary, tag string) (Result, error) {
	result := Result{From: u.Current, To: tag, Dest: u.Dest}

	if _, err := os.Stat(u.Dest); err == nil {
		// On unix the running file can be renamed while the process runs,
		// because the kernel keeps the open inode. On Windows a running .exe
		// cannot be deleted but can be renamed, so the same order works there.
		previous := u.Dest + "." + versionSuffix(u.Current)
		_ = os.Remove(previous)
		if err := os.Rename(u.Dest, previous); err != nil {
			return Result{}, fmt.Errorf("cannot move the current binary aside "+
				"(is %s writable?): %w", filepath.Dir(u.Dest), err)
		}
		result.Previous = previous
	}

	if err := moveFile(binary, u.Dest); err != nil {
		if result.Previous != "" {
			// Put the working binary back rather than leaving nothing there.
			if back := os.Rename(result.Previous, u.Dest); back == nil {
				return Result{}, fmt.Errorf("could not install the new binary, "+
					"the previous one is back in place: %w", err)
			}
		}
		return Result{}, fmt.Errorf("could not install the new binary: %w", err)
	}
	if err := os.Chmod(u.Dest, 0o755); err != nil {
		return result, err
	}
	return result, nil
}

// Rollback puts a kept binary back. It exists so a caller that finds the new
// binary broken has one call to undo the update.
func (u *Updater) Rollback(result Result) error {
	if result.Previous == "" {
		return fmt.Errorf("no previous binary was kept")
	}
	return os.Rename(result.Previous, result.Dest)
}

func (u *Updater) fetch(ctx context.Context, tag, asset, dest string) error {
	url := fmt.Sprintf("%s/%s/%s", strings.TrimRight(u.Download, "/"), tag, asset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := u.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", asset, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s is not in release %s (HTTP %d)", asset, tag, resp.StatusCode)
	}
	file, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, io.LimitReader(resp.Body, maxAsset)); err != nil {
		file.Close()
		return fmt.Errorf("downloading %s: %w", asset, err)
	}
	return file.Close()
}

// verifyWithCosign is the real verifier. cosign is required rather than
// optional: an update mechanism nobody can verify is worse than none.
func verifyWithCosign(ctx context.Context, sums, bundle, repo string) error {
	if _, err := exec.LookPath("cosign"); err != nil {
		return fmt.Errorf("cosign is required to verify a release but is not installed.\n" +
			"Install it from https://docs.sigstore.dev/system_config/installation/ - " +
			"an unverified update mechanism on a deployment worker is worse than none.\n" +
			"Until then, update by hand from the release page.")
	}
	cmd := exec.CommandContext(ctx, "cosign", "verify-blob", sums,
		"--bundle", bundle,
		"--certificate-identity-regexp", fmt.Sprintf(identityPattern, repo),
		"--certificate-oidc-issuer", oidcIssuer)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("the checksum file is not signed by %s's release workflow, "+
			"refusing: %w: %s", repo, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// checksumFor reads one entry out of a SHA256SUMS file.
func checksumFor(path, asset string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		// sha256sum writes "*name" for a binary-mode entry.
		if strings.TrimPrefix(fields[1], "*") == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%s is not listed in SHA256SUMS", asset)
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// moveFile renames, and copies when the rename crosses a filesystem.
func moveFile(from, to string) error {
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}

// versionSuffix keeps the kept-binary name usable: "dev" and tags are fine,
// anything with a separator in it is not.
func versionSuffix(version string) string {
	if version == "" {
		return "previous"
	}
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.' || r == '-' || r == '_' || r == '~' || r == '+':
			return r
		}
		return -1
	}, version)
	if clean == "" {
		return "previous"
	}
	return clean
}
