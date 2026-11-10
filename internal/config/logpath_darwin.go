package config

// LogPath is where the daemon's log goes.
func LogPath() (string, error) {
	return HomeJoin("Library", "Logs", "lichen.log")
}
