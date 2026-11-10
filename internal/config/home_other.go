//go:build !windows

package config

func spelled(abs, home string) string { return abs }
