package atomicfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentWritersPublishWholeFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			data := bytes.Repeat([]byte{byte('a' + i)}, 4096)
			if err := Write(path, data, 0600); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 4096 || !bytes.Equal(data, bytes.Repeat(data[:1], 4096)) {
		t.Fatal("mixed or partial state")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("temporary files retained: %v %v", files, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state permissions: %v %v", info, err)
	}
}
func TestFailedRenameCleansTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "destination")
	os.Mkdir(path, 0700)
	if err := Write(path, []byte("state"), 0600); err == nil {
		t.Fatal("replaced directory")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("temporary file leak: %v %v", files, err)
	}
}

func TestPublicationFailureDistinguishesVisibleState(t *testing.T) {
	for _, replace := range []bool{true, false} {
		path := filepath.Join(t.TempDir(), "state")
		failure := errors.New("directory sync failed")
		err := publish(path, []byte("new state"), 0600, replace, func(string) error { return failure })
		var published *PublishedError
		if !errors.As(err, &published) || !errors.Is(err, failure) {
			t.Fatalf("publication phase lost: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "new state" {
			t.Fatalf("published state missing: %q %v", data, err)
		}
	}
	path := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	var published *PublishedError
	if err := Write(path, nil, 0600); err == nil || errors.As(err, &published) {
		t.Fatalf("failed rename misclassified: %v", err)
	}
}
