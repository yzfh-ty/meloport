package cloudsync

import "golang.org/x/sys/windows"

func moveStaged(from, to string) error {
	source, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	// Without COPY_ALLOWED or REPLACE_EXISTING, a conflicting destination
	// or cross-volume move leaves the working file in place.
	return windows.MoveFileEx(source, destination, 0)
}
