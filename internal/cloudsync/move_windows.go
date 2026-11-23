package cloudsync

import "golang.org/x/sys/windows"

func renameNoReplace(from, to string) error {
	source, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	// Both names are on the mounted destination volume. Never replace an
	// existing file or silently fall back to a non-atomic filesystem copy.
	return windows.MoveFileEx(source, destination, 0)
}
