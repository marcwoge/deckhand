//go:build windows

package deploy

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// replaceLink points linkPath at target. Windows only allows symlinks for
// unprivileged users when Developer Mode is on, so directories fall back to a
// junction, which always works.
func replaceLink(linkPath, target string, isDir bool) error {
	_ = os.Remove(linkPath)
	if err := os.Symlink(target, linkPath); err == nil {
		return nil
	}
	if !isDir {
		return fmt.Errorf("cannot create link %s: enable Developer Mode or run as administrator", linkPath)
	}
	cmd := exec.Command("cmd", "/C", "mklink", "/J", linkPath, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mklink /J %s: %v: %s", linkPath, err, out)
	}
	return nil
}

// linkInto links dst to src, degrading to a hard link and finally a copy when
// the filesystem or the user's privileges do not allow links.
func linkInto(dst, src string, isDir bool) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	_ = os.RemoveAll(dst)
	if err := os.Symlink(src, dst); err == nil {
		return nil
	}
	if isDir {
		cmd := exec.Command("cmd", "/C", "mklink", "/J", dst, src)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("mklink /J %s: %v: %s", dst, err, out)
		}
		return nil
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
