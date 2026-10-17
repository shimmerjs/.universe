package cgdisplay

import (
	"fmt"
	"sync"
)

// APIError preserves the boundary and original CGError. Recovery policy must
// distinguish a broken connection from invalid arguments and unknown codes.
type APIError struct {
	Boundary string `json:"boundary"`
	Code     int32  `json:"code"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("CoreGraphics %s: %s (%d)", e.Boundary, e.Kind(), e.Code)
}

func (e *APIError) Kind() string {
	switch e.Code {
	case 1000:
		return "failure"
	case 1001:
		return "illegal_argument"
	case 1002:
		return "invalid_connection"
	case 1003:
		return "invalid_context"
	case 1004:
		return "cannot_complete"
	case 1006:
		return "not_implemented"
	case 1007:
		return "range_check"
	case 1008:
		return "type_check"
	case 1010:
		return "invalid_operation"
	case 1011:
		return "none_available"
	default:
		return "unknown"
	}
}

type notificationAPI struct {
	register func(uintptr) int32
	remove   func(uintptr) int32
}

var notifications = struct {
	sync.Mutex
	next uintptr
	live map[uintptr]*Subscription
}{live: make(map[uintptr]*Subscription)}

// NotificationStats separates callback delivery from consumer processing.
type NotificationStats struct {
	Begin      uint64 `json:"begin"`
	Completed  uint64 `json:"completed"`
	AfterClose uint64 `json:"after_close"`
}

// Subscription coalesces completed reconfigurations into a pending wakeup.
// The owner must run the application's event loop; successful registration
// alone does not establish callback delivery. Changes is never closed.
type Subscription struct {
	Changes   <-chan struct{}
	changes   chan struct{}
	mu        sync.Mutex
	stats     NotificationStats
	accepting bool
	closeMu   sync.Mutex
	removed   bool
	id        uintptr
	api       notificationAPI
}

func subscribe(api notificationAPI) (*Subscription, error) {
	ch := make(chan struct{}, 1)
	s := &Subscription{Changes: ch, changes: ch, accepting: true, api: api}
	notifications.Lock()
	notifications.next++
	s.id = notifications.next
	notifications.live[s.id] = s
	notifications.Unlock()
	if code := api.register(s.id); code != 0 {
		notifications.Lock()
		delete(notifications.live, s.id)
		notifications.Unlock()
		return nil, &APIError{Boundary: "register", Code: code}
	}
	return s, nil
}

func deliverNotification(id uintptr, flags uint32) {
	notifications.Lock()
	s := notifications.live[id]
	notifications.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.accepting {
		s.stats.AfterClose++
		return
	}
	if flags&1 != 0 { // kCGDisplayBeginConfigurationFlag
		s.stats.Begin++
		return
	}
	s.stats.Completed++
	select {
	case s.changes <- struct{}{}:
	default:
	}
}

func (s *Subscription) Stats() NotificationStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Close fences delivery before unregistering. On removal failure, the token
// remains valid and Close can be retried. No freed Go pointer reaches C.
func (s *Subscription) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.removed {
		return nil
	}
	s.mu.Lock()
	s.accepting = false
	s.mu.Unlock()
	if code := s.api.remove(s.id); code != 0 {
		return &APIError{Boundary: "remove", Code: code}
	}
	notifications.Lock()
	delete(notifications.live, s.id)
	notifications.Unlock()
	s.removed = true
	return nil
}

func (e *APIError) NativeCode() int { return int(e.Code) }
