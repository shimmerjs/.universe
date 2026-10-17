package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"
)

const (
	clientQueueLen      = 128
	maxBroadcastClients = 64
	clientWriteWait     = 5 * time.Second
)

// broadcaster fans frames out to touch.sock clients as ndjson lines. Each
// client gets a bounded queue. Overflow disconnects it so consumers clear
// held input state rather than silently losing a release. Retained
// lines (last line per key) replay to a new client at accept, so a consumer
// connecting between publishes still sees current state (battery, mapping
// state) instead of waiting a poll interval.
type broadcaster struct {
	ln         net.Listener
	mu         sync.Mutex
	clients    map[net.Conn]chan []byte
	retained   map[string][]byte
	owner      *os.File
	done       chan struct{}
	acceptDone chan struct{}
	writers    sync.WaitGroup
	closeOnce  sync.Once
}

// newBroadcaster listens on path, creating the parent dir 0700 and replacing
// a stale socket. Refuses to start if another daemon is serving the path.
var newBroadcaster = listenBroadcaster

func listenBroadcaster(path string) (*broadcaster, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	owner, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	claimed := false
	defer func() {
		if !claimed {
			owner.Close()
		}
	}()
	if err := syscall.Flock(int(owner.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, fmt.Errorf("%s owner lock: %w", path, err)
	}
	conn, probeErr := net.DialTimeout("unix", path, 500*time.Millisecond)
	if probeErr == nil {
		conn.Close()
		return nil, fmt.Errorf("%s already in use", path)
	}
	switch {
	case errors.Is(probeErr, fs.ErrNotExist):
	case errors.Is(probeErr, syscall.ECONNREFUSED):
		// A full Darwin socket backlog can refuse connections while still alive.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cmd := exec.CommandContext(ctx, "/usr/sbin/lsof", "-t", "--", path)
		cmd.WaitDelay = time.Second
		out, err := cmd.Output()
		cancel()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || len(out) != 0 || len(exit.Stderr) != 0 {
			return nil, fmt.Errorf("%s holder not proven absent: %v", path, err)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%s probe: %w", path, probeErr)
	}
	// umask-wrapped so the socket is NEVER world-connectable, even in the
	// listen-to-chmod window (a connect completed there outlives the chmod;
	// keys.sock is a keystroke feed). Process-global but newBroadcaster
	// runs single-threaded at construction.
	old := syscall.Umask(0o077)
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	// normalize to owner-only 0600, matching the bus's khudson.sock
	// (the umask wrap above already denies group/world)
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("tighten socket: %w", err)
	}
	claimed = true
	b := &broadcaster{owner: owner, done: make(chan struct{}), acceptDone: make(chan struct{}), ln: ln, clients: map[net.Conn]chan []byte{}, retained: map[string][]byte{}}
	go b.accept()
	return b, nil
}

func (b *broadcaster) accept() {
	defer close(b.acceptDone)
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return
		}
		ch := make(chan []byte, clientQueueLen)
		b.mu.Lock()
		if len(b.clients) >= maxBroadcastClients {
			b.mu.Unlock()
			conn.Close()
			continue
		}
		b.clients[conn] = ch
		// retained replay rides the same critical section as registration:
		// publishJSON holds mu, so a concurrent publish lands strictly after
		// the replay -- never reordered, never duplicated-before
		for _, key := range slices.Sorted(maps.Keys(b.retained)) {
			offer(ch, b.retained[key])
		}
		b.mu.Unlock()
		peerGone := make(chan struct{})
		b.writers.Go(func() {
			// This feed is one-way. EOF or unexpected client data ends its lifetime.
			var buf [1]byte
			conn.Read(buf[:])
			close(peerGone)
			conn.Close()
		})
		b.writers.Go(func() { b.writeTo(conn, ch, peerGone) })
	}
}

func (b *broadcaster) writeTo(conn net.Conn, ch chan []byte, peerGone <-chan struct{}) {
	defer func() {
		b.mu.Lock()
		delete(b.clients, conn)
		b.mu.Unlock()
		conn.Close()
	}()
	for {
		select {
		case <-b.done:
			return
		case <-peerGone:
			return
		case line := <-ch:
			err := conn.SetWriteDeadline(time.Now().Add(clientWriteWait))
			if err == nil {
				var n int
				n, err = conn.Write(line)
				if err == nil && n != len(line) {
					err = io.ErrShortWrite
				}
			}
			if err != nil {
				hidBoundaries.Observe("broadcast.write", err, "disconnect client; client must reconnect")
				return
			}
		}
	}

}

// publishJSON queues a complete frame or disconnects a lagging client.
func (b *broadcaster) publishJSON(v any) {
	b.publishLine(v, "")
}

// publishRetainedJSON publishes v and retains it under key: future clients
// receive the last line per key at accept.
func (b *broadcaster) publishRetainedJSON(key string, v any) {
	b.publishLine(v, key)
}

func (b *broadcaster) publishLine(v any, retainKey string) {
	line, err := json.Marshal(v)
	if err != nil {
		hidBoundaries.Observe("broadcast.encode", err, "drop invalid frame")
		return
	}
	line = append(line, '\n')
	b.mu.Lock()
	if retainKey != "" {
		b.retained[retainKey] = line
	}
	overflow := false
	for conn, ch := range b.clients {
		select {
		case ch <- line:
		default:
			conn.Close()
			delete(b.clients, conn)
			overflow = true
		}
	}
	b.mu.Unlock()
	if overflow {
		hidBoundaries.Observe("broadcast.queue", errors.New("client queue full"), "disconnect client; reconnect clears held input")
	}
}

func offer(ch chan []byte, line []byte) {
	select {
	case ch <- line:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- line:
	default:
	}
}

func (b *broadcaster) clientCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.clients)
}

// close unlinks the socket (via listener close) and hangs up every client.
func (b *broadcaster) close() {
	b.closeOnce.Do(func() {
		close(b.done)
		b.ln.Close()
		<-b.acceptDone
		b.mu.Lock()
		for conn := range b.clients {
			conn.Close()
		}
		b.mu.Unlock()
		b.writers.Wait()
		b.owner.Close()
	})
}
