package config

import (
	"os"

	"golang.org/x/sys/windows"
)

// replace puts tmp in path's place, as a rename does on Unix. Windows
// will not rename over a read-only file, so that one stops being
// read-only first: tmp carries its permissions, and makes it read-only
// again. What else Windows marks a file as, hidden or system, goes over
// to tmp, which would otherwise show a hidden file again.
func replace(tmp, path string) error {
	from, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	was, err := windows.GetFileAttributes(to)
	if err != nil {
		// Nothing there to replace.
		return os.Rename(tmp, path)
	}
	if kept := was & (windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM); kept != 0 {
		if attrs, err := windows.GetFileAttributes(from); err == nil {
			windows.SetFileAttributes(from, attrs|kept)
		}
	}
	if was&windows.FILE_ATTRIBUTE_READONLY != 0 {
		windows.SetFileAttributes(to, was&^windows.FILE_ATTRIBUTE_READONLY)
	}
	if err := os.Rename(tmp, path); err != nil {
		windows.SetFileAttributes(to, was)
		return err
	}
	return nil
}
