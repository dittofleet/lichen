// Package backup implements lichen's overwrite policy. Backups are off
// by default: a file lichen has to get out of the way (a pre-existing
// local file it is about to apply over, a copy deleted on another
// machine) is simply removed. With "backups": true in the config, the
// old version is moved to
// ~/lichen-backups/<timestamp>/<home-relative-path> instead. Deliberately
// visible in the home directory (not Documents: that often syncs to
// iCloud, and backed-up dotfiles can contain secrets). Backups are never
// auto-deleted.
package backup

import (
	"os"
	"path/filepath"
	"time"

	"lichen/internal/config"
)

// batch groups everything backed up by one process run under a single
// timestamped directory.
var batch string

func Root() (string, error) {
	home, err := config.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "lichen-backups"), nil
}

// destPath maps abs (always under home: both callers guarantee it) to its
// home-relative spot in the current batch directory.
func destPath(abs string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	home, _ := config.Home()
	rel, _ := filepath.Rel(home, abs)
	if batch == "" {
		batch = time.Now().Format("2006-01-02-150405")
	}
	dst := filepath.Join(root, batch, rel)
	return dst, os.MkdirAll(filepath.Dir(dst), 0o755)
}

// Discard gets abs out of the way: moved into the backup area
// (preserving its home-relative layout) when keep is set, removed
// otherwise. A directory is always kept: lichen syncs files, so one in
// the way holds files that exist nowhere else. Returns the backup
// location, "" when removed.
func Discard(abs string, keep bool) (string, error) {
	fi, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if !keep && !fi.IsDir() {
		return "", os.Remove(abs)
	}
	dst, err := destPath(abs)
	if err != nil {
		return "", err
	}
	return dst, os.Rename(abs, dst)
}

// Copy snapshots abs into the backup area without moving it, for files
// that must stay in place (e.g. the moment they start being managed). A
// directory is copied recursively (regular files only).
func Copy(abs string) (string, error) {
	dst, err := destPath(abs)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return dst, copyFile(abs, dst, fi.Mode().Perm())
	}
	err = filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, _ := filepath.Rel(abs, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFile(p, target, info.Mode().Perm())
	})
	return dst, err
}

func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}

// NonEmpty reports whether any backups exist (for status output).
func NonEmpty() bool {
	root, err := Root()
	if err != nil {
		return false
	}
	des, err := os.ReadDir(root)
	return err == nil && len(des) > 0
}
