package viewtransport

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/socketio"
	"github.com/shimmerjs/khudson/khudson/surface"
)

type busClient struct {
	net.Conn
	writer *socketio.Writer
	ch     chan proto.Msg
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
}

func newBusClient(conn net.Conn) *busClient {
	c := &busClient{Conn: conn, ch: make(chan proto.Msg, 16), stop: make(chan struct{}), done: make(chan struct{})}
	c.writer = socketio.NewWriter(conn, 128, 2*time.Second, func(err error) {
		if err != nil {
			c.Close()
		}
		diagnostic.Default.Observe("view.bus.write", err, "disconnect; resync; do not replay actions")
	})
	go c.read()
	return c
}

func (c *busClient) Close() error {
	c.once.Do(func() { close(c.stop); c.writer.Close() })
	return nil
}

func (c *busClient) wait() { <-c.done; c.writer.Wait() }

func (c *busClient) read() {
	defer close(c.done)
	defer close(c.ch)
	defer c.Close()
	dec := socketio.NewDecoder(c.Conn)
	for {
		var msg proto.Msg
		if err := dec.Decode(&msg); err != nil {
			select {
			case <-c.stop:
			default:
				diagnostic.Default.Observe("view.bus.read", err, "disconnect; await reconnect")
			}
			return
		}
		diagnostic.Default.Observe("view.bus.read", nil, "receiving")
		select {
		case c.ch <- msg:
		case <-c.stop:
			return
		}
	}
}

// busSession owns dial commands and clients, including connections whose
// completion messages never reach Update because the terminal exited.
type busSession struct {
	ctx      context.Context
	cancel   context.CancelFunc
	dial     func(context.Context, string, string) (net.Conn, error)
	mu       sync.Mutex
	closed   bool
	wg       sync.WaitGroup
	delivery *deliveryWorker
}

func newBusSession() *busSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &busSession{ctx: ctx, cancel: cancel, dial: (&net.Dialer{Timeout: time.Second}).DialContext}
}

func (s *busSession) close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
	if s.delivery != nil {
		s.delivery.close()
	}
}

func (s *busSession) connect(gen int, socket string, hello proto.Msg) tea.Cmd {
	return func() tea.Msg {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return surface.Disconnected{Gen: gen, Err: context.Canceled}
		}
		s.wg.Add(1)
		s.mu.Unlock()
		defer s.wg.Done()
		conn, err := s.dial(s.ctx, "unix", socket)
		if err != nil {
			if s.ctx.Err() == nil {
				diagnostic.Default.Observe("view.bus.dial", err, "retry")
			}
			return surface.Disconnected{Gen: gen, Err: err}
		}
		diagnostic.Default.Observe("view.bus.dial", nil, "connected")
		client := newBusClient(conn)
		stop := context.AfterFunc(s.ctx, func() { client.Close() })
		s.wg.Add(1)
		go func() { defer s.wg.Done(); client.wait(); stop() }()
		if err := client.writer.Send(hello); err != nil {
			client.Close()
			return surface.Disconnected{Gen: gen, Err: err}
		}
		return surface.Connected{Gen: gen, Conn: client, Ch: client.ch}
	}
}

var errDeliveryFull = errors.New("durable delivery queue full")

type deliveryItem struct {
	msg    proto.Msg
	client *busClient
}
type deliveryWorker struct {
	journal *actionjournal.Store
	queue   chan deliveryItem
	done    chan struct{}
	cancel  context.CancelFunc
}

func newDeliveryWorker(store *actionjournal.Store) *deliveryWorker {
	ctx, cancel := context.WithCancel(context.Background())
	d := &deliveryWorker{journal: store, queue: make(chan deliveryItem, 128), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(d.done)
		for {
			select {
			case <-ctx.Done():
				return
			case item := <-d.queue:
				if ctx.Err() != nil {
					return
				}
				d.deliver(ctx, item)
			}
		}
	}()
	return d
}
func (d *deliveryWorker) close() { d.cancel(); <-d.done }
func (d *deliveryWorker) submit(m proto.Msg, client *busClient) error {
	select {
	case <-d.done:
		return context.Canceled
	default:
	}
	select {
	case d.queue <- deliveryItem{m, client}:
		return nil
	default:
		return errDeliveryFull
	}
}
func (d *deliveryWorker) deliver(ctx context.Context, item deliveryItem) {
	m := item.msg
	payload := m
	payload.OperationID = ""
	data, err := json.Marshal(payload)
	source := m.Widget
	if source == "" {
		source = m.Cmd
	}
	if err == nil {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, _, err = d.journal.Accept(bounded, actionjournal.Request{ID: m.OperationID, Kind: m.Type, Source: source, Policy: actionjournal.DiscardOnRestart, Payload: data})
		cancel()
	}
	diagnostic.Default.Observe("view.action.persist", err, "refuse send unless committed")
	if err == nil {
		diagnostic.Action("view.action.persist", m.OperationID, "", m.ViewID, m.ViewGeneration, "pending", "committed before transport")
		err = item.client.writer.Send(m)
		diagnostic.Default.Observe("view.action.send", err, "retain pending record; never replay stale view actions")
	}
	if err != nil {
		// Closing the connection makes failed acceptance visible through the
		// normal reconnect path. A committed pending record remains inspectable.
		item.client.Close()
	}
}

func New(store *actionjournal.Store) *busSession {
	s := newBusSession()
	s.delivery = newDeliveryWorker(store)
	return s
}
func (s *busSession) Context() context.Context { return s.ctx }
func (s *busSession) Close()                   { s.close() }
func (s *busSession) Connect(gen int, socket string, hello proto.Msg) tea.Cmd {
	return s.connect(gen, socket, hello)
}
func (c *busClient) Send(m proto.Msg) error { return c.writer.Send(m) }
func (s *busSession) Send(m proto.Msg, connection surface.Connection) error {
	c, ok := connection.(*busClient)
	if !ok {
		return errors.New("unknown transport connection")
	}
	if m.Type == proto.TypeRowAct || m.Type == proto.TypeAction || m.Type == proto.TypeCtl {
		return s.delivery.submit(m, c)
	}
	return c.Send(m)
}
