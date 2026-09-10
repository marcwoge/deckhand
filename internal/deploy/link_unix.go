//go:build !windows

package deploy

import (
	"os"
	"path/filepath"
)

// replaceLink points linkPath at target, replacing whatever was there. On unix
// the swap is atomic: the new link is created under a temporary name and then
// renamed over the old one.
func replaceLink(linkPath, target string, _ bool) error {
	tmp := linkPath + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, linkPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// linkInto creates a link at dst pointing to src (used for shared paths).
func linkInto(dst, src string, isDir bool) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	_ = os.RemoveAll(dst)
	return os.Symlink(src, dst)
}
