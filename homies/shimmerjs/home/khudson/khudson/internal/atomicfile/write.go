// Package atomicfile publishes complete state files using same-directory renames.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write syncs a private temporary file before replacing path, then syncs the
// directory entry. A failure after rename means the new data may be visible.
// Callers must distinguish a failed persistence acknowledgement from no change.
func Write(path string, data []byte, mode os.FileMode) error {
	return publish(path, data, mode, true, syncDirectory)
}

// Create publishes without replacing an existing file, including under races.
func Create(path string, data []byte, mode os.FileMode) error {
	return publish(path, data, mode, false, syncDirectory)
}

// PublishedError means publication succeeded but its durability acknowledgement
// failed. Retrying a non-idempotent operation on this basis is unsafe.
type PublishedError struct{ Err error }

func (e *PublishedError) Error() string {
	return "state published but durability unconfirmed: " + e.Err.Error()
}
func (e *PublishedError) Unwrap() error        { return e.Err }
func (*PublishedError) OutcomeUncertain() bool { return true }

func publish(path string, data []byte, mode os.FileMode, replace bool, syncDir func(string) error) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if replace {
		err = os.Rename(name, path)
	} else {
		err = os.Link(name, path)
	}
	if err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		return &PublishedError{Err: err}
	}
	return nil
}

func syncDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	closeErr := d.Close()
	if err != nil {
		return err
	}
	return closeErr
}
