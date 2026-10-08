// Package app is lichen's description of itself for go-cli-kit.
package app

import (
	clikit "github.com/dittofleet/go-cli-kit"

	"lichen/internal/version"
)

// New describes this build to go-cli-kit, which finds its releases under
// dittofleet/lichen.
func New() clikit.App {
	return clikit.App{Name: "lichen", Version: version.Current}
}
