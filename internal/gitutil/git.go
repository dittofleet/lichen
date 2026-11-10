// Package gitutil shells out to the system git rather than reimplementing
// it: lichen's correctness story leans on git's fetch/merge semantics, and
// every machine lichen targets already has git.
package gitutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// gitTimeout bounds every git subprocess: a black-holed network (a laptop
// that changed Wi-Fi mid-push) otherwise blocks in read() for hours, and
// because the caller holds the cross-process lock, that hang would wedge
// the whole daemon. A generous ceiling: real pushes and pulls finish well
// inside it.
const gitTimeout = 3 * time.Minute

// settings are how git behaves in every repo lichen keeps, whatever the
// device's own git config says.
var settings = []string{
	// Files are the same on every device byte for byte, so line endings
	// are never converted on the way in or out, as Git for Windows does
	// unless told not to.
	"-c", "core.autocrlf=false", "-c", "core.eol=lf",
	// Git for Windows otherwise stops at paths of 260 characters.
	"-c", "core.longpaths=true",
}

func Run(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", slices.Concat(settings, args)...)
	cmd.Dir = dir
	// A git that is killed can leave a helper of its own behind, holding
	// the output open for good: on Windows, one unpacking a fetch never
	// learns its git is gone.
	cmd.WaitDelay = 5 * time.Second
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := strings.TrimSpace(buf.String())
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return out, fmt.Errorf("git %s: timed out after %s", strings.Join(args, " "), gitTimeout)
		}
		return out, fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, out)
	}
	return out, nil
}
