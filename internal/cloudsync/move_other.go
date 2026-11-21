//go:build !windows && !linux && !darwin

package cloudsync

import "errors"

func moveStaged(_, _ string) error {
	return errors.New("atomic CD2 staging is not supported on this operating system")
}
