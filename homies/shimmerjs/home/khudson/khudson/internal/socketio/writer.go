// Package socketio owns bounded asynchronous JSON writes to a connection.
package socketio

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

type BackpressureError struct{}

func (BackpressureError) Kind() string { return "socket_backpressure" }

func (BackpressureError) Error() string { return "socket output queue full" }

// Writer preserves enqueue order. Send copies a message before returning;
// callers may reuse its backing data. Overflow closes the connection so a
// consumer cannot continue with an incomplete state stream. Actions are never
// replayed. Successful enqueue is not an acknowledgement from the peer.
type Writer struct {
	conn         net.Conn
	grace        time.Duration
	queue        chan []byte
	stop         chan struct{}
	done         chan struct{}
	mu           sync.Mutex
	err          error
	pendingBytes int
	observe      func(error)
}

func NewWriter(conn net.Conn, capacity int, grace time.Duration, observe func(error)) *Writer {
	w := &Writer{conn: conn, grace: grace, queue: make(chan []byte, capacity), stop: make(chan struct{}), done: make(chan struct{}), observe: observe}
	go w.run()
	return w
}

// Send performs serialization and a bounded enqueue, never socket I/O.
func (w *Writer) Send(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		w.fail(err)
		return err
	}
	if len(data)+1 > MaxFrameBytes {
		err = FrameSizeError{}
		w.fail(err)
		return err
	}
	w.mu.Lock()
	if w.err != nil {
		err = w.err
	} else if w.pendingBytes+len(data)+1 > maxPendingBytes {
		err = BackpressureError{}
		w.stopLocked(err)
	} else {
		select {
		case w.queue <- append(data, '\n'):
			w.pendingBytes += len(data) + 1
		default:
			err = BackpressureError{}
			w.stopLocked(err)
		}
	}
	w.mu.Unlock()
	return err
}

func (w *Writer) stopLocked(err error) {
	if w.err == nil {
		w.err = err
		close(w.stop)
	}
}

func (w *Writer) fail(err error) {
	w.mu.Lock()
	w.stopLocked(err)
	w.mu.Unlock()
}

// Close cancels queued writes. Wait joins both workers and connection closure.
func (w *Writer) Close() { w.fail(net.ErrClosed) }
func (w *Writer) Wait()  { <-w.done }
func (w *Writer) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *Writer) run() {
	closed := make(chan struct{})
	go func() {
		<-w.stop
		w.conn.Close() // also interrupts an in-flight write and the peer reader
		close(closed)
	}()
	defer func() {
		w.Close()
		<-closed
		// Drop retained payloads even if a caller keeps the stopped writer.
		for len(w.queue) > 0 {
			<-w.queue
		}
		if err := w.Err(); w.observe != nil && !errors.Is(err, net.ErrClosed) {
			w.observe(err)
		}
		close(w.done)
	}()
	for {
		select {
		case <-w.stop:
			return
		case data := <-w.queue:
			if w.Err() != nil {
				return
			}
			err := w.conn.SetWriteDeadline(time.Now().Add(w.grace))
			if err == nil {
				var n int
				n, err = w.conn.Write(data)
				if err == nil && n != len(data) {
					err = io.ErrShortWrite
				}
			}
			if err != nil {
				w.fail(err)
				return
			}
			w.mu.Lock()
			w.pendingBytes -= len(data)
			w.mu.Unlock()
			if w.observe != nil {
				w.observe(nil)
			}
		}
	}
}
