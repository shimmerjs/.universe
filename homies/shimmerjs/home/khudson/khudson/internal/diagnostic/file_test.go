package diagnostic

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLogRotationBoundsConcurrentClients(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostics.jsonl")
	record := []byte(strings.Repeat("x", 255) + "\n")
	// Contended clients may drop enough writes that no rotation occurs.
	// Establish both generations before testing concurrent append/rotation.
	seed, err := OpenFile(path, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	for range 17 {
		if _, err := seed.Write(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		f, err := OpenFile(path, 4096)
		if err != nil {
			t.Fatal(err)
		}
		wg.Go(func() {
			defer f.Close()
			for range 50 {
				if _, err := f.Write(record); err != nil && !errors.Is(err, unix.EWOULDBLOCK) {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	for _, name := range []string{path, path + ".1"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > 4096 || info.Mode().Perm() != 0600 {
			t.Fatalf("bad retained log: %v", info)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if len(line) != 255 && !json.Valid([]byte(line)) {
				t.Fatal("concurrent append corrupted record")
			}
		}
	}
}

func TestContendedLogDoesNotWaitForOtherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	f, err := OpenFile(path, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	holder, err := os.OpenFile(path+".lock", os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := unix.Flock(int(holder.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("lost\n")); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("contended write=%v", err)
	}
	unix.Flock(int(holder.Fd()), unix.LOCK_UN)
	if _, err := f.Write([]byte("kept\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "lost\n") || !strings.Contains(string(data), `"count":1`) || !strings.HasSuffix(string(data), "kept\n") {
		t.Fatalf("loss accounting: %s", data)
	}
}
