package diagnostic

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"

	"github.com/mattn/go-sqlite3"
)

func TestDistinctErrorsAndRecovery(t *testing.T) {
	var events []Event
	r := New(func(e Event) { events = append(events, e) })
	for range 5 {
		r.Observe("connect", fmt.Errorf("dial: %w", syscall.ECONNREFUSED), "backoff")
	}
	r.Observe("connect", &os.PathError{Op: "dial", Path: "x", Err: syscall.EACCES}, "repair_permissions")
	r.Observe("connect", errors.New("unrecognized"), "backoff")
	r.Observe("connect", nil, "resume")
	if len(events) != 6 || events[0].Kind != "connection_refused" || events[3].Kind != "permission" || events[5].Outcome != "recovered" {
		t.Fatal(events)
	}
	if events[0].Code == nil || *events[0].Code != int(syscall.ECONNREFUSED) {
		t.Fatal("raw code lost")
	}
}

func TestSQLiteErrorKeepsExtendedCode(t *testing.T) {
	var got Event
	r := New(func(e Event) { got = e })
	r.Observe("actions.accept", sqlite3.Error{Code: sqlite3.ErrBusy, ExtendedCode: sqlite3.ErrBusy.Extend(2)}, "retry without execution")
	if got.Kind != "sqlite_busy" || got.Code == nil || *got.Code != int(sqlite3.ErrBusy.Extend(2)) {
		t.Fatalf("lost sqlite code: %+v", got)
	}
}

type nativeFailure struct{ code int }

func (e nativeFailure) Error() string   { return "native failure" }
func (e nativeFailure) Kind() string    { return "unknown_native" }
func (e nativeFailure) NativeCode() int { return e.code }
func TestNativeCodesRemainDistinctThroughWrapping(t *testing.T) {
	var events []Event
	r := New(func(e Event) { events = append(events, e) })
	r.Observe("display", fmt.Errorf("query: %w", nativeFailure{1007}), "retain invalidation")
	r.Observe("display", fmt.Errorf("query: %w", nativeFailure{1008}), "retain invalidation")
	if len(events) != 2 || events[0].Count != 1 || events[1].Count != 1 || events[0].Code == nil || *events[0].Code != 1007 || events[1].Code == nil || *events[1].Code != 1008 {
		t.Fatalf("codes merged or lost: %+v", events)
	}
}

func TestJoinedNativeFailuresEmitEveryCode(t *testing.T) {
	var events []Event
	r := New(func(e Event) { events = append(events, e) })
	r.Observe("ax.hidden", fmt.Errorf("partial hide: %w", errors.Join(nativeFailure{1007}, nativeFailure{1008})), "retain partial outcome")
	if len(events) != 2 || events[0].Code == nil || *events[0].Code != 1007 || events[1].Code == nil || *events[1].Code != 1008 {
		t.Fatalf("joined failures lost: %+v", events)
	}
}
