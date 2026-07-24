package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"
)

const (
	clientQueueLen  = 128
	clientWriteWait = 5 * time.Second
)

// broadcaster fans frames out to touch.sock clients as ndjson lines. Each
// client gets a bounded drop-oldest queue so a slow client never blocks the
// device read loop; a stuck client is dropped via write deadline. Retained
// lines (last line per key) replay to a new client at accept, so a consumer
// connecting between publishes still sees current state (battery, mapping
// state) instead of waiting a poll interval.
type broadcaster struct {
	ln       net.Listener
	mu       sync.Mutex
	clients  map[net.Conn]chan []byte
	retained map[string][]byte
}

// newBroadcaster listens on path, creating the parent dir 0700 and replacing
// a stale socket. Refuses to start if another daemon is serving the path.
func newBroadcaster(path string) (*broadcaster, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if conn, err := net.Dial("unix", path); err == nil {
		conn.Close()
		return nil, fmt.Errorf("%s already in use (another touchd?)", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
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
	b := &broadcaster{ln: ln, clients: map[net.Conn]chan []byte{}, retained: map[string][]byte{}}
	go b.accept()
	return b, nil
}

func (b *broadcaster) accept() {
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return
		}
		ch := make(chan []byte, clientQueueLen)
		b.mu.Lock()
		b.clients[conn] = ch
		// retained replay rides the same critical section as registration:
		// publishJSON holds mu, so a concurrent publish lands strictly after
		// the replay -- never reordered, never duplicated-before
		for _, key := range slices.Sorted(maps.Keys(b.retained)) {
			offer(ch, b.retained[key])
		}
		b.mu.Unlock()
		go b.writeTo(conn, ch)
	}
}

func (b *broadcaster) writeTo(conn net.Conn, ch chan []byte) {
	defer func() {
		b.mu.Lock()
		delete(b.clients, conn)
		b.mu.Unlock()
		conn.Close()
	}()
	for line := range ch {
		conn.SetWriteDeadline(time.Now().Add(clientWriteWait))
		if _, err := conn.Write(line); err != nil {
			return
		}
	}
}

// publishJSON marshals v and queues the ndjson line to every client without
// blocking: a full queue evicts its oldest frame. The eviction can race the
// client's writer draining the queue, in which case the retried send may
// still miss -- the frame is dropped for that client rather than looping.
// The keys socket shares the fanout.
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
		return
	}
	line = append(line, '\n')
	b.mu.Lock()
	defer b.mu.Unlock()
	if retainKey != "" {
		b.retained[retainKey] = line
	}
	for _, ch := range b.clients {
		offer(ch, line)
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
	b.ln.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	for conn := range b.clients {
		conn.Close()
	}
}
