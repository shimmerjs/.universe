package diagnostic

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// File retains the current log and one previous generation. The lock file
// coordinates append and rotation across independent terminal clients.
// Contention drops the write rather than blocking service work; the next
// successful write records how many writes were lost.
type File struct {
	mu      sync.Mutex
	lock    *os.File
	path    string
	limit   int64
	dropped uint64
}

func OpenFile(path string, limit int64) (*File, error) {
	if limit < 4096 {
		return nil, errors.New("diagnostic log limit must be at least 4096")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	return &File{lock: f, path: path, limit: limit}, nil
}

func (f *File) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lock == nil {
		return 0, os.ErrClosed
	}
	if err := unix.Flock(int(f.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.dropped++
		return 0, err
	}
	defer unix.Flock(int(f.lock.Fd()), unix.LOCK_UN)
	n := len(p)
	if f.dropped > 0 {
		marker := []byte(fmt.Sprintf("{\"boundary\":\"diagnostic.file\",\"outcome\":\"dropped\",\"count\":%d,\"action\":\"log lock contended; service continued\"}\n", f.dropped))
		budget := int(f.limit) - len(marker)
		if len(p) > budget {
			p = append(append([]byte(nil), p[:budget-1]...), '\n')
		}
		p = append(marker, p...)
	}
	if int64(n) > f.limit {
		p = append(append([]byte(nil), p[:f.limit-1]...), '\n')
	}
	info, err := os.Stat(f.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if info != nil && info.Size()+int64(len(p)) > f.limit {
		if err := os.Rename(f.path, f.path+".1"); err != nil {
			return 0, err
		}
	}
	out, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	written, writeErr := out.Write(p)
	closeErr := out.Close()
	if writeErr != nil {
		return 0, writeErr
	}
	if written != len(p) {
		return 0, io.ErrShortWrite
	}
	if closeErr == nil {
		f.dropped = 0
	}
	return n, closeErr
}

func (f *File) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lock == nil {
		return nil
	}
	err := f.lock.Close()
	f.lock = nil
	return err
}
