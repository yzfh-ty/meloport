package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Ensure creates only a missing local configuration so the Web UI can bootstrap.
func Ensure(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect configuration: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("create configuration: %w", err)
	}
	_, writeErr := file.WriteString("# Meloport local configuration. Manage subscriptions in the Web UI.\n# Keys and optional platform cookies are read from environment variables.\ntasks: []\n")
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
