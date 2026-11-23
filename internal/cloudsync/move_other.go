//go:build !windows && !linux && !darwin

package cloudsync

import "errors"

func renameNoReplace(_, _ string) error {
	return errors.New("atomic CloudDrive2 publication is not supported on this operating system")
}
