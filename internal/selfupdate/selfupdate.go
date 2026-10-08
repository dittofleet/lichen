// Package selfupdate installs the lichen release the sync repo requires:
// the version gate drives it when the sync repo outversions this build.
package selfupdate

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/dittofleet/go-cli-kit/release"
	kitupdate "github.com/dittofleet/go-cli-kit/selfupdate"

	"lichen/internal/app"
	"lichen/internal/version"
)

// Required installs the latest release for a build the sync repo has
// outversioned, first verifying the release actually satisfies repoV: a
// deleted release could otherwise have every machine downloading a build
// that is still too old, forever.
func Required(repoV string) (string, error) {
	lichen := app.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tag, err := release.FetchLatestTag(ctx, lichen)
	if err != nil {
		return "", fmt.Errorf("fetching release info: %w", err)
	}
	// The release workflow only tags vX.Y.Z, and Compare needs that shape.
	if !version.Valid(tag) {
		return "", fmt.Errorf("latest release tag %q is not vX.Y.Z", tag)
	}
	if version.Compare(tag, repoV) < 0 {
		return "", fmt.Errorf("the sync repo requires %s but the latest release is %s: publish a newer release, or lower %s in the sync repo", repoV, tag, version.Marker)
	}
	if err := kitupdate.Install(ctx, lichen, tag); err != nil {
		return "", err
	}
	return tag, nil
}

// ExecSelf replaces this process with the binary at its own path (after
// an Install, that is the new build), preserving arguments and
// environment (callers os.Setenv anything the new process must inherit,
// which keeps repeated execs from stacking duplicate entries). Returns
// only on failure.
func ExecSelf() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(self, os.Args, os.Environ())
}
