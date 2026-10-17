package renoservice

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/effectrun"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/socketio"
	module "github.com/shimmerjs/khudson/khudson/provider"
)

type fakeBackend struct{ calls atomic.Int32 }

func (*fakeBackend) Name() string { return "reno" }
func (*fakeBackend) Poll(context.Context, map[string]any) (module.Data, error) {
	return module.Data{}, nil
}
func (f *fakeBackend) HandleActEffects([]string) (bool, []module.Effect) {
	f.calls.Add(1)
	return true, []module.Effect{{Key: "test", Argv: []string{"renoman", "reno", "fake"}}}
}
func (f *fakeBackend) HandleCtl([]string) (any, []module.Effect, error) {
	f.calls.Add(1)
	return map[string]any{"ok": true}, nil, nil
}
func TestLostAcknowledgementCannotRepeatTransition(t *testing.T) {
	store, err := actionjournal.Open(filepath.Join(t.TempDir(), "actions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend := &fakeBackend{}
	o := &owner{backend: backend, journal: store}
	var executions atomic.Int32
	o.effects = effectrun.New(effectrun.Hooks{Accept: o.accept, Transition: o.transition, Finish: func(m proto.Msg, s actionjournal.State, r string) {
		if err := o.transition(m, actionjournal.Running, s, r); err != nil {
			t.Error(err)
		}
	}, Reject: func(_ proto.Msg, e error) { t.Error(e) }, Broadcast: func(proto.Msg) {}, Failed: func([]string, error) { t.Error("effect failed") }, Start: func() func([]string) (func() error, error) {
		return func([]string) (func() error, error) { executions.Add(1); return func() error { return nil }, nil }
	}})
	go o.effects.Run()
	defer o.effects.Shutdown()
	m := proto.Msg{Type: proto.TypeRowAct, OperationID: "same", Argv: []string{"reno:next"}, ViewID: "view", ViewGeneration: 3}
	if resp := o.handle(context.Background(), m); !resp.OK {
		t.Fatal(resp.Error)
	}
	o.effects.Flush()
	if resp := o.handle(context.Background(), m); !resp.OK {
		t.Fatal(resp.Error)
	}
	o.effects.Flush()
	if backend.calls.Load() != 1 || executions.Load() != 1 {
		t.Fatal("lost ack replayed mutation")
	}
	records, err := store.List(context.Background(), "")
	if err != nil || len(records) != 2 {
		t.Fatalf("operation and child not recorded: %v %v", records, err)
	}
}

type blockedControlBackend struct {
	fakeBackend
	release chan struct{}
}

func (b *blockedControlBackend) HandleCtl([]string) (any, []module.Effect, error) {
	b.calls.Add(1)
	<-b.release
	return nil, nil, nil
}

func TestConcurrentDuplicateDoesNotReportDefiniteFailure(t *testing.T) {
	store, err := actionjournal.Open(filepath.Join(t.TempDir(), "actions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend := &blockedControlBackend{release: make(chan struct{})}
	o := &owner{backend: backend, journal: store, effects: effectrun.New(effectrun.Hooks{})}
	go o.effects.Run()
	defer o.effects.Shutdown()
	var clients sync.WaitGroup
	defer clients.Wait()
	unblock := sync.OnceFunc(func() { close(backend.release) })
	defer unblock()
	m := proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"next"}, OperationID: "concurrent"}
	if _, _, err := o.accept(m, actionjournal.Review); err != nil {
		t.Fatal(err)
	}
	const count = 16
	start := make(chan struct{})
	responses := make(chan proto.Msg, count)
	for range count {
		clients.Go(func() {
			<-start
			responses <- o.handle(context.Background(), m)
		})
	}
	close(start)
	for range count - 1 {
		select {
		case response := <-responses:
			if response.OK || (response.OperationState != string(actionjournal.Running) && response.OperationState != string(actionjournal.Uncertain)) {
				t.Fatalf("duplicate lost the in-flight outcome: %+v", response)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("duplicate request blocked on the backend")
		}
	}
	unblock()
	select {
	case response := <-responses:
		if !response.OK || backend.calls.Load() != 1 {
			t.Fatalf("winner = %+v, executions = %d", response, backend.calls.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("winning request did not finish")
	}
}

func TestFailedClaimRetainsUncertainOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.sqlite")
	store, err := actionjournal.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db, err := sql.Open("khudson-actionjournal", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TRIGGER fail_claim BEFORE UPDATE OF state ON operations
 WHEN OLD.state = 'pending' AND NEW.state = 'running'
 BEGIN SELECT RAISE(FAIL, 'claim unavailable'); END`)
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{}
	o := &owner{backend: backend, journal: store}
	response := o.handle(context.Background(), proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"next"}})
	if response.OK || response.OperationState != string(actionjournal.Uncertain) || backend.calls.Load() != 0 {
		t.Fatalf("claim failure lost uncertainty: %+v, executions = %d", response, backend.calls.Load())
	}
}

func TestOwnerExclusionShutdownAndRestartRecovery(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "reno-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	store, err := actionjournal.Open(filepath.Join(dir, "actions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Accept(context.Background(), actionjournal.Request{ID: "interrupted", Kind: "ctl", Source: "test", Policy: actionjournal.Review, Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Transition(context.Background(), "interrupted", actionjournal.Pending, actionjournal.Running, ""); err != nil {
		t.Fatal(err)
	}
	store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	constructed := make(chan struct{})
	done := make(chan error, 1)
	backend := &fakeBackend{}
	go func() { done <- Run(ctx, dir, func() Backend { close(constructed); return backend }) }()
	select {
	case <-constructed:
	case err := <-done:
		t.Fatalf("owner startup: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("startup timeout")
	}
	if err = Run(ctx, dir, func() Backend { t.Error("second owner constructed backend"); return backend }); err == nil {
		t.Fatal("second owner accepted")
	}
	socket := filepath.Join(dir, "owner.sock")
	response, err := Call(ctx, socket, proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"jobs"}})
	if err != nil {
		t.Fatal(err)
	}
	var page actionjournal.Page
	if err = json.Unmarshal(response.Data, &page); err != nil {
		t.Fatal(err)
	}
	records := page.Records
	if len(records) != 1 || records[0].State != actionjournal.Uncertain || backend.calls.Load() != 0 {
		t.Fatalf("recovery replayed or lost action: %+v", records)
	}
	if _, err = Call(ctx, socket, proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"discard-job", "interrupted"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = Call(ctx, socket, proto.Msg{Type: "invalid"}); err == nil {
		t.Fatal("invalid request accepted")
	}
	idle, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("idle client prevented shutdown")
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	err = Run(ctx2, dir, func() Backend { cancel2(); return backend })
	if err != nil {
		t.Fatalf("owner lock leaked across shutdown: %v", err)
	}
}

// Close must be exercised through the service, not just on the backend itself.
type closingBackend struct {
	fakeBackend
	closing  chan struct{}
	release  chan struct{}
	closeErr error
}

func (b *closingBackend) Close() error { close(b.closing); <-b.release; return b.closeErr }

func TestServiceJoinsBackendBeforeReleasingOwnership(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "reno-close-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closeErr := errors.New("backend cleanup failed")
	backend := &closingBackend{closing: make(chan struct{}), release: make(chan struct{}), closeErr: closeErr}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- Run(ctx, dir, func() Backend { close(started); return backend }) }()
	<-started
	cancel()
	select {
	case <-backend.closing:
	case <-time.After(3 * time.Second):
		t.Fatal("service never closed backend")
	}
	// Unblock even if the ownership assertion fails.
	defer close(backend.release)
	select {
	case err := <-done:
		t.Fatalf("returned before backend closed: %v", err)
	default:
	}
	if err := Run(context.Background(), dir, func() Backend { t.Error("replacement constructed during cleanup"); return &fakeBackend{} }); err == nil {
		t.Fatal("released ownership before backend closed")
	}
	backend.release <- struct{}{}
	select {
	case err := <-done:
		if !errors.Is(err, closeErr) {
			t.Fatalf("lost close failure: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not join backend")
	}
}

type pollingBackend struct {
	fakeBackend
	entered chan struct{}
	release chan struct{}
	polls   atomic.Int32
}

func (b *pollingBackend) Poll(ctx context.Context, _ map[string]any) (module.Data, error) {
	b.polls.Add(1)
	select {
	case b.entered <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return module.Data{}, nil
	case <-ctx.Done():
		return module.Data{}, ctx.Err()
	}
}
func TestCancelledPollDoesNotWaitForBusyBackend(t *testing.T) {
	backend := &pollingBackend{entered: make(chan struct{}, 1), release: make(chan struct{})}
	o := &owner{backend: backend, pollGate: make(chan struct{}, 1)}
	request := proto.Msg{Type: "poll", Data: []byte(`{}`)}
	first := make(chan proto.Msg, 1)
	go func() { first <- o.handle(context.Background(), request) }()
	<-backend.entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response := make(chan proto.Msg, 1)
	go func() { response <- o.handle(ctx, request) }()
	select {
	case r := <-response:
		if r.OK {
			t.Fatal("cancelled poll reported success")
		}
	case <-time.After(time.Second):
		close(backend.release)
		t.Fatal("cancelled poll waited behind backend")
	}
	close(backend.release)
	<-first
	if backend.polls.Load() != 1 {
		t.Fatal("expired queued poll reached backend")
	}
}

func TestJournalInspectionPagesCrossTransport(t *testing.T) {
	store, err := actionjournal.Open(filepath.Join(t.TempDir(), "actions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// HTML escaping expands this valid, maximum-sized payload on the wire.
	// An unpaged response exceeds the decoder's frame limit.
	payload := []byte(`"` + strings.Repeat("<", 60*1024) + `"`)
	for i := 0; i < 32; i++ {
		_, _, err := store.Accept(context.Background(), actionjournal.Request{ID: strconv.Itoa(i), Kind: "fixture", Source: "test", Policy: actionjournal.Review, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
	}
	o := &owner{journal: store}
	cursor := int64(0)
	seen := make(map[string]bool)
	for iteration := 0; iteration < 33; iteration++ {
		server, peer := net.Pipe()
		done := make(chan struct{})
		go func() { defer close(done); defer server.Close(); o.serve(context.Background(), server) }()
		peer.SetDeadline(time.Now().Add(3 * time.Second))
		enc := json.NewEncoder(peer)
		if err := enc.Encode(proto.Msg{Type: proto.TypeHello, Role: proto.RoleCtl}); err != nil {
			t.Fatal(err)
		}
		if err := enc.Encode(proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"jobs", strconv.FormatInt(cursor, 10)}}); err != nil {
			t.Fatal(err)
		}
		var response proto.Msg
		err := socketio.NewDecoder(peer).Decode(&response)
		peer.Close()
		<-done
		if err != nil || !response.OK {
			t.Fatalf("inspection transport: %v %s", err, response.Error)
		}
		var page actionjournal.Page
		if err := json.Unmarshal(response.Data, &page); err != nil {
			t.Fatal(err)
		}
		if len(response.Data) > 1<<20 {
			t.Fatalf("page exceeded byte budget: %d", len(response.Data))
		}
		for _, record := range page.Records {
			if seen[record.ID] {
				t.Fatal("duplicate inspection record")
			}
			seen[record.ID] = true
			var decoded string
			if err := json.Unmarshal(record.Payload, &decoded); err != nil || len(decoded) != 60*1024 {
				t.Fatal("inspection truncated payload")
			}
		}
		if page.Next == 0 {
			if len(seen) != 32 {
				t.Fatalf("lost records: %d", len(seen))
			}
			return
		}
		if page.Next <= cursor {
			t.Fatal("cursor did not advance")
		}
		cursor = page.Next
	}
	t.Fatal("inspection never reached final page")
}

func TestRequestDeadlineCancelsBackendPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := &pollingBackend{entered: make(chan struct{}, 1), release: make(chan struct{})}
		o := &owner{backend: backend, pollGate: make(chan struct{}, 1)}
		server, peer := net.Pipe()
		defer server.Close()
		defer peer.Close()
		done := make(chan struct{})
		go func() { defer close(done); o.serve(context.Background(), server) }()
		enc := json.NewEncoder(peer)
		if err := enc.Encode(proto.Msg{Type: proto.TypeHello, Role: proto.RoleCtl}); err != nil {
			t.Fatal(err)
		}
		if err := enc.Encode(proto.Msg{Type: "poll", Data: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
		<-backend.entered
		time.Sleep(16 * time.Second)
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("socket deadline left backend polling")
		}
	})
}

func TestDisconnectedPollCancelsBackend(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := &pollingBackend{entered: make(chan struct{}, 1), release: make(chan struct{})}
		o := &owner{backend: backend, pollGate: make(chan struct{}, 1)}
		server, peer := net.Pipe()
		defer server.Close()
		defer peer.Close()
		done := make(chan struct{})
		go func() { defer close(done); o.serve(context.Background(), server) }()
		enc := json.NewEncoder(peer)
		if err := enc.Encode(proto.Msg{Type: proto.TypeHello, Role: proto.RoleCtl}); err != nil {
			t.Fatal(err)
		}
		if err := enc.Encode(proto.Msg{Type: "poll", Data: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
		<-backend.entered
		peer.Close()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("disconnected view left a backend poll running")
		}
	})
}

// A successful mutation may still produce a response that cannot be encoded.
type invalidResponseBackend struct {
	fakeBackend
	beforeReturn func()
}

func (*invalidResponseBackend) Poll(context.Context, map[string]any) (module.Data, error) {
	return module.Data{Rows: []module.Row{{Series: []float64{math.NaN()}}}}, nil
}
func (b *invalidResponseBackend) HandleCtl([]string) (any, []module.Effect, error) {
	b.calls.Add(1)
	if b.beforeReturn != nil {
		b.beforeReturn()
	}
	return math.NaN(), nil, nil
}

func TestEncodingFailurePreservesCommittedOutcome(t *testing.T) {
	store, err := actionjournal.Open(filepath.Join(t.TempDir(), "actions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend := &invalidResponseBackend{}
	o := &owner{backend: backend, journal: store, pollGate: make(chan struct{}, 1), effects: effectrun.New(effectrun.Hooks{})}
	go o.effects.Run()
	defer o.effects.Shutdown()
	for _, m := range []proto.Msg{
		{Type: "poll", Data: []byte(`{}`)},
		{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"status"}},
		{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"next"}, OperationID: "invalid-data"},
	} {
		response := o.handle(context.Background(), m)
		if response.OK || !strings.Contains(response.Error, "NaN") {
			t.Fatalf("encoding failure lost: %+v", response)
		}
		if m.OperationID != "" {
			if response.OperationState != string(actionjournal.Dispatched) {
				t.Fatalf("committed outcome lost: %+v", response)
			}
			if retry := o.handle(context.Background(), m); !retry.OK || backend.calls.Load() != 2 {
				t.Fatalf("repeated mutation: %+v calls=%d", retry, backend.calls.Load())
			}
		}
	}
}

func TestFailedOutcomeCommitReportsUncertain(t *testing.T) {
	store, err := actionjournal.Open(filepath.Join(t.TempDir(), "actions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend := &invalidResponseBackend{beforeReturn: func() { store.Close() }}
	o := &owner{backend: backend, journal: store, effects: effectrun.New(effectrun.Hooks{})}
	go o.effects.Run()
	defer o.effects.Shutdown()
	response := o.handle(context.Background(), proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"next"}, OperationID: "commit-lost"})
	if response.OK || response.OperationState != string(actionjournal.Uncertain) || response.Error == "" {
		t.Fatalf("lost outcome misreported: %+v", response)
	}
}

func TestExpiredMutationDoesNotReachBackend(t *testing.T) {
	store, err := actionjournal.Open(filepath.Join(t.TempDir(), "actions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend := &fakeBackend{}
	o := &owner{backend: backend, journal: store, effects: effectrun.New(effectrun.Hooks{})}
	go o.effects.Run()
	defer o.effects.Shutdown()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response := o.handle(ctx, proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"next"}})
	if response.OK || backend.calls.Load() != 0 {
		t.Fatalf("expired mutation executed: %+v calls=%d", response, backend.calls.Load())
	}
	// The provider never ran, so the journal must not demand review of an unknown outcome.
	records, err := store.List(context.Background(), "")
	if err != nil || len(records) != 1 || records[0].State != actionjournal.Failed || response.OperationState != string(actionjournal.Failed) {
		t.Fatalf("unstarted mutation recorded as %+v (response %+v) err=%v", records, response, err)
	}
}

type refusingBackend struct{ fakeBackend }

func (b *refusingBackend) HandleActEffects([]string) (bool, []module.Effect) {
	b.calls.Add(1)
	return false, nil
}

// Callers classify Client errors with actionjournal.ExecutionState. Requests the
// owner never received or definitely refused must not read as lost replies.
func TestClientActKeepsDefiniteFailureDistinctFromLostReply(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "reno-act-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	argv := []string{"reno:next"}
	absent := &Client{Socket: filepath.Join(dir, "absent.sock")}
	if err := absent.Act(context.Background(), argv, module.Operation{ID: "absent"}); err == nil || actionjournal.ExecutionState(err) != actionjournal.Failed {
		t.Fatalf("absent owner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &refusingBackend{}
	ownerDir := filepath.Join(dir, "owner")
	constructed := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- Run(ctx, ownerDir, func() Backend { close(constructed); return backend }) }()
	select {
	case <-constructed:
	case err := <-done:
		t.Fatalf("owner startup: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("startup timeout")
	}
	refused := &Client{Socket: filepath.Join(ownerDir, "owner.sock")}
	if err := refused.Act(ctx, argv, module.Operation{ID: "refused"}); err == nil || actionjournal.ExecutionState(err) != actionjournal.Failed || backend.calls.Load() != 1 {
		t.Fatalf("refused action: %v calls=%d", err, backend.calls.Load())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	lostSocket := filepath.Join(dir, "lost.sock")
	listener, err := net.Listen("unix", lostSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		dec := json.NewDecoder(conn)
		var hello, request proto.Msg
		_ = dec.Decode(&hello)
		_ = dec.Decode(&request)
	}()
	if err := (&Client{Socket: lostSocket}).Act(context.Background(), argv, module.Operation{ID: "lost"}); actionjournal.ExecutionState(err) != actionjournal.Uncertain {
		t.Fatalf("lost reply: %v", err)
	}
}

func TestCallDistinguishesUnknownOutcomeFromReadFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		request   proto.Msg
		mismatch  bool
		uncertain bool
	}{
		{"lost-mutation", proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"next"}}, false, true},
		{"lost-status", proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"status"}}, false, false},
		{"wrong-ack", proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"next"}}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "reno-wire-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "owner.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				dec := json.NewDecoder(conn)
				var hello, request proto.Msg
				if dec.Decode(&hello) != nil || dec.Decode(&request) != nil {
					return
				}
				if tc.mismatch {
					json.NewEncoder(conn).Encode(proto.Msg{Type: proto.TypeResp, OK: true, OperationID: "someone-else"})
				}
			}()
			response, err := Call(context.Background(), socket, tc.request)
			<-done
			if err == nil || errors.Is(err, socketio.ErrUncertainDelivery) != tc.uncertain {
				t.Fatalf("outcome error: %v", err)
			}
			if tc.uncertain && (response.OperationID == "" || response.OperationState != string(actionjournal.Uncertain)) {
				t.Fatalf("missing recovery identity: %+v", response)
			}
		})
	}
}

type rejectedEffectBackend struct{ fakeBackend }

func (b *rejectedEffectBackend) HandleActEffects([]string) (bool, []module.Effect) {
	b.calls.Add(1)
	return true, []module.Effect{{Key: "invalid", Argv: []string{"unexpected-program"}}}
}
func TestFailedEffectAdmissionRetainsPartialTransition(t *testing.T) {
	store, err := actionjournal.Open(filepath.Join(t.TempDir(), "actions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend := &rejectedEffectBackend{}
	o := &owner{backend: backend, journal: store, effects: effectrun.New(effectrun.Hooks{Broadcast: func(proto.Msg) {}, Failed: func([]string, error) {}})}
	go o.effects.Run()
	defer o.effects.Shutdown()
	response := o.handle(context.Background(), proto.Msg{Type: proto.TypeRowAct, OperationID: "partial-transition", Argv: []string{"reno:next"}})
	records, err := store.List(context.Background(), actionjournal.Uncertain)
	if response.OK || response.OperationState != string(actionjournal.Uncertain) || backend.calls.Load() != 1 || err != nil || len(records) != 1 {
		t.Fatalf("partial transition lost: %+v calls=%d records=%+v err=%v", response, backend.calls.Load(), records, err)
	}
	o.handle(context.Background(), proto.Msg{Type: proto.TypeRowAct, OperationID: "partial-transition", Argv: []string{"reno:next"}})
	if backend.calls.Load() != 1 {
		t.Fatal("failed dispatch repeated provider transition")
	}
}
