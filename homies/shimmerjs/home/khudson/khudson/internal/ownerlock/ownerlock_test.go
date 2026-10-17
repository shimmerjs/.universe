package ownerlock

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestOwnershipSurvivesUntilClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := Acquire(path); !errors.Is(err, ErrHeld) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("second owner: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}
