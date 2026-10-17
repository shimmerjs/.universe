package cgdisplay

import (
	"errors"
	"sync"
	"testing"
)

func quietAPI() notificationAPI {
	return notificationAPI{register: func(uintptr) int32 { return 0 }, remove: func(uintptr) int32 { return 0 }}
}

func TestRegistrationErrorsKeepBoundaryAndCode(t *testing.T) {
	for _, code := range []int32{1000, 1001, 1002, 1003, 1004, 1006, 1007, 1008, 1010, 1011, 9876} {
		api := quietAPI()
		var token uintptr
		api.register = func(id uintptr) int32 { token = id; return code }
		s, err := subscribe(api)
		var detail *APIError
		if s != nil || !errors.As(err, &detail) || detail.Code != code || detail.Boundary != "register" {
			t.Fatalf("code %d: subscription=%v error=%v", code, s, err)
		}
		notifications.Lock()
		_, leaked := notifications.live[token]
		notifications.Unlock()
		if leaked {
			t.Fatal("failed registration retained a live token")
		}
	}
}

func TestNotificationPhasesAndCoalescing(t *testing.T) {
	api := quietAPI()
	// Delivery may happen before registration returns.
	api.register = func(id uintptr) int32 { deliverNotification(id, 1); return 0 }
	s, err := subscribe(api)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case <-s.Changes:
		t.Fatal("begin phase requested a snapshot")
	default:
	}
	for range 100 {
		deliverNotification(s.id, 0)
	}
	if got := s.Stats(); got.Begin != 1 || got.Completed != 100 {
		t.Fatalf("stats: %+v", got)
	}
	select {
	case <-s.Changes:
	default:
		t.Fatal("completed change lost")
	}
	select {
	case <-s.Changes:
		t.Fatal("burst was not coalesced")
	default:
	}
	deliverNotification(s.id, 1<<5) // removal does not require querying its obsolete ID
	select {
	case <-s.Changes:
	default:
		t.Fatal("removal lost")
	}
}

func TestRemovalFailureRetainsFencedTokenForRetry(t *testing.T) {
	api := quietAPI()
	calls := 0
	api.remove = func(id uintptr) int32 {
		calls++
		deliverNotification(id, 0)
		if calls == 1 {
			return 1002
		}
		return 0
	}
	s, err := subscribe(api)
	if err != nil {
		t.Fatal(err)
	}
	var detail *APIError
	if err := s.Close(); !errors.As(err, &detail) || detail.Boundary != "remove" || detail.Code != 1002 {
		t.Fatalf("remove: %v", err)
	}
	select {
	case <-s.Changes:
		t.Fatal("callback delivered during removal")
	default:
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil || calls != 2 {
		t.Fatalf("idempotent close: %v, calls=%d", err, calls)
	}
	if s.Stats().AfterClose != 2 {
		t.Fatalf("late callbacks: %+v", s.Stats())
	}
	next, err := subscribe(quietAPI())
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	deliverNotification(s.id, 0)
	select {
	case <-next.Changes:
		t.Fatal("old callback reached new subscription")
	default:
	}
}

func TestConcurrentDeliveryAndClose(t *testing.T) {
	s, err := subscribe(quietAPI())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				deliverNotification(s.id, 0)
			}
		}()
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	// A callback already pending at Close is allowed; later callbacks are fenced.
	select {
	case <-s.Changes:
	default:
	}
	deliverNotification(s.id, 0)
	select {
	case <-s.Changes:
		t.Fatal("closed subscription received a callback")
	default:
	}
}
