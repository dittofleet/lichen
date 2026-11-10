//go:build !darwin

package config

// LogPath is where the daemon's log goes.
func LogPath() (string, error) {
	return HomeJoin(".local", "state", "lichen", "lichen.log")
}
