package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"lichen/internal/version"
)

// marker records, in the sync folder, the newest lichen version that has
// synced with it.
const marker = ".lichen/version"

// OutdatedError refuses a pass because this build is older than the
// version recorded in the sync folder: a stale build must never capture,
// apply, or push over what a newer device wrote. Callers react by
// self-updating.
type OutdatedError struct{ Build, Repo string }

func (e *OutdatedError) Error() string {
	return fmt.Sprintf("this build (%s) is older than the sync repo requires (%s): syncing is paused until it updates", e.Build, e.Repo)
}

// Gate refuses with *OutdatedError when the sync folder's marker
// outversions this build. Dev builds are exempt: they cannot be ordered
// against release tags, nor replaced by the self-updater.
func (e *Env) Gate() error {
	repoV, err := e.markerVersion()
	if err != nil {
		return err
	}
	return gateAgainst(repoV)
}

func gateAgainst(repoV string) error {
	if version.IsRelease() && repoV != "" && version.Compare(version.Current, repoV) < 0 {
		return &OutdatedError{Build: version.Current, Repo: repoV}
	}
	return nil
}

// SyncVersion is the post-pull pass over the marker: undo a rebase
// downgrade, refuse if this build is behind, and record this build if it
// is ahead (committed, so the pass's push tells every other device to
// update). Recording is skipped when the pull failed: a bump computed
// against a marker that could not be refreshed may be stale.
func (e *Env) SyncVersion(pulled bool) error {
	e.repairMarker()
	repoV, err := e.markerVersion()
	if err != nil {
		return err
	}
	if err := gateAgainst(repoV); err != nil {
		return err
	}
	if !pulled || !version.IsRelease() || (repoV != "" && version.Compare(version.Current, repoV) <= 0) {
		return nil
	}
	if err := os.MkdirAll(e.Path(".lichen"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(e.Path(marker), []byte(version.Current+"\n"), 0o644); err != nil {
		return err
	}
	e.Log.Printf("recording lichen %s in the sync folder", version.Current)
	_, err = e.Commit("version "+version.Current, "")
	return err
}

// repairMarker undoes a marker downgrade left by a rebase: the conflict
// policy keeps OUR replayed commits (local wins), but the marker's
// semantics are max-wins, so a concurrent bump from a newer device must
// be restored from upstream.
func (e *Env) repairMarker() {
	up, err := e.Git("show", "@{u}:"+marker)
	if err != nil {
		return
	}
	upV := strings.TrimSpace(up)
	if !version.Valid(upV) {
		return
	}
	local, err := e.markerVersion()
	if err == nil && local != "" && version.Compare(local, upV) >= 0 {
		return
	}
	if os.WriteFile(e.Path(marker), []byte(upV+"\n"), 0o644) != nil {
		return
	}
	e.Log.Printf("restoring version marker %s from upstream (a rebase kept a stale copy)", upV)
	e.Commit("restore version "+upV, "")
}

// markerVersion reads the marker: "" when absent or malformed (no
// requirement), an error when it exists but cannot be read. An unreadable
// marker must not silently disable the gate.
func (e *Env) markerVersion() (string, error) {
	data, err := os.ReadFile(e.Path(marker))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if v := strings.TrimSpace(string(data)); version.Valid(v) {
		return v, nil
	}
	return "", nil
}
