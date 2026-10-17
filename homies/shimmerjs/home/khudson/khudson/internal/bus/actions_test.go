package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/renoservice"
	"github.com/shimmerjs/khudson/khudson/internal/socketio"
	module "github.com/shimmerjs/khudson/khudson/provider"
)

func journalBus(t *testing.T) *Bus {
	t.Helper()
	b, _, _ := schedTestBus(t)
	b.opts.Paths.Dir = t.TempDir()
	b.openActions()
	if b.journalErr != nil {
		t.Fatal(b.journalErr)
	}
	t.Cleanup(func() {
		if b.journal != nil {
			b.journal.Close()
		}
	})
	return b
}
func TestUnavailableJournalRefusesInputExecution(t *testing.T) {
	b, _ := inputTestBus()
	b.journalRequired = true
	b.journalErr = errors.New("storage unavailable")
	calls := 0
	b.execStart = func([]string) (func() error, error) { calls++; return func() error { return nil }, nil }
	b.input = make(chan proto.Msg, 1)
	b.input <- proto.Msg{Type: proto.TypeAction, Widget: "w", Arg: "tap"}
	close(b.input)
	b.inputWorkerContext(context.Background())
	if calls != 0 {
		t.Fatal("executed an action without durable acceptance")
	}
}
func TestDesiredStateRecoversWithoutReplayingSideEffects(t *testing.T) {
	b := journalBus(t)
	b.cfg.Layouts["other"] = config.Layout{Kind: "dock-grid", Tiles: []string{"w"}}
	msg := proto.Msg{Type: proto.TypeCtl, Cmd: "layout", Arg: "other"}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = b.journal.Accept(context.Background(), actionjournal.Request{ID: "desired", Kind: proto.TypeCtl, Source: "layout", Policy: actionjournal.Reconcile, Payload: data})
	if err != nil {
		t.Fatal(err)
	}
	if err = b.journal.Transition(context.Background(), "desired", actionjournal.Pending, actionjournal.Running, ""); err != nil {
		t.Fatal(err)
	}
	b.journal.Close()
	b.openActions()
	if b.journalErr != nil {
		t.Fatal(b.journalErr)
	}
	b.recoverActions()
	if b.cfg.Layout != "other" {
		t.Fatal("desired layout did not reconcile")
	}
	rows, err := b.journal.List(context.Background(), "")
	if err != nil || len(rows) != 1 || rows[0].State != actionjournal.Succeeded {
		t.Fatalf("recovery = %+v %v", rows, err)
	}
}
func TestDuplicateControlDoesNotExecuteAgain(t *testing.T) {
	b := journalBus(t)
	// Absolute layout commands are deterministic; use the journal state itself
	// to ensure a repeated ID does not run through the state machine twice.
	m := proto.Msg{Type: proto.TypeCtl, Cmd: "layout", Arg: "main", OperationID: "same"}
	if r := b.executeCtl(m); !r.OK {
		t.Fatal(r.Error)
	}
	first, err := b.journal.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if r := b.executeCtl(m); !r.OK {
		t.Fatal(r.Error)
	}
	second, err := b.journal.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].UpdatedNS != first[0].UpdatedNS {
		t.Fatal("duplicate operation executed again")
	}
	m.Arg = "missing"
	if r := b.executeCtl(m); r.OK {
		t.Fatal("operation id accepted with changed payload")
	}
}
func TestJournalFailureLeavesBusAvailableForInspection(t *testing.T) {
	b := journalBus(t)
	b.journal.Close()
	b.journal = nil
	b.journalErr = errors.New("corrupt journal")
	if r := b.executeCtl(proto.Msg{Cmd: "status"}); !r.OK {
		t.Fatal("journal failure disabled read-only status")
	}
	if r := b.executeCtl(proto.Msg{Cmd: "layout", Arg: "main"}); r.OK {
		t.Fatal("journal failure permitted state mutation")
	}
}

func TestPrecommittedActionClaimedExactlyOnce(t *testing.T) {
	b := journalBus(t)
	m := proto.Msg{Type: proto.TypeCtl, Cmd: "layout", Arg: b.cfg.Layout, OperationID: "client-persisted"}
	payload := m
	payload.OperationID = ""
	raw, _ := json.Marshal(payload)
	_, _, err := b.journal.Accept(context.Background(), actionjournal.Request{ID: m.OperationID, Kind: m.Type, Source: m.Cmd, Policy: actionjournal.Reconcile, Payload: raw})
	if err != nil {
		t.Fatal(err)
	}
	if resp := b.executeCtl(m); !resp.OK {
		t.Fatal(resp.Error)
	}
	first, _ := b.journal.List(context.Background(), "")
	if resp := b.executeCtl(m); !resp.OK {
		t.Fatal(resp.Error)
	}
	second, _ := b.journal.List(context.Background(), "")
	if len(second) != 1 || second[0].State != actionjournal.Succeeded || second[0].UpdatedNS != first[0].UpdatedNS {
		t.Fatalf("duplicate reexecuted: %+v", second)
	}
}

func TestEffectJournalRetainsParentAndView(t *testing.T) {
	b := journalBus(t)
	b.effects = b.newEffectRunner()
	parent := proto.Msg{OperationID: "parent", ViewID: "surface", ViewGeneration: 7}
	err := b.dispatchEffectsFor(parent, "reno", []module.Effect{{Key: "focus", Argv: []string{"khudson", "reno", "focus", "target"}}})
	if err != nil {
		t.Fatal(err)
	}
	records, err := b.journal.List(context.Background(), "")
	if err != nil || len(records) != 1 {
		t.Fatalf("records %v %v", records, err)
	}
	var got proto.Msg
	if err := json.Unmarshal(records[0].Payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.ParentOperationID != parent.OperationID || got.ViewID != parent.ViewID || got.ViewGeneration != 7 {
		t.Fatalf("lost correlation: %+v", got)
	}
}

func TestRejectedPrecommittedIntentIsDiscarded(t *testing.T) {
	b := journalBus(t)
	m := proto.Msg{Type: proto.TypeRowAct, Widget: "w", Argv: []string{"fake"}, OperationID: "retired-intent", ViewID: "gone", ViewGeneration: 1}
	payload := m
	payload.OperationID = ""
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = b.journal.Accept(context.Background(), actionjournal.Request{ID: m.OperationID, Kind: m.Type, Source: m.Widget, Policy: actionjournal.DiscardOnRestart, Payload: raw})
	if err != nil {
		t.Fatal(err)
	}
	b.discardInput(m, "view retired")
	records, err := b.journal.List(context.Background(), actionjournal.Discarded)
	if err != nil || len(records) != 1 || records[0].Reason != "view retired" {
		t.Fatalf("rejection lost: %+v %v", records, err)
	}
}

func TestInterruptedActionRemainsUncertain(t *testing.T) {
	b := journalBus(t)
	m, _, err := b.acceptAction(proto.Msg{Type: proto.TypeAction, Widget: "w", OperationID: "interrupted-action"}, actionjournal.DiscardOnRestart)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.transitionAction(m, actionjournal.Pending, actionjournal.Running, ""); err != nil {
		t.Fatal(err)
	}
	b.reapAction(m, "fixture", []string{"fixture"}, func() error { return context.DeadlineExceeded })
	records, err := b.journal.List(context.Background(), actionjournal.Uncertain)
	if err != nil || len(records) != 1 || records[0].ID != m.OperationID {
		t.Fatalf("interruption lost: %+v %v", records, err)
	}
}

func TestRecoveryDoesNotOverwriteNewerDesiredState(t *testing.T) {
	for _, completed := range []bool{false, true} {
		name := "pending"
		if completed {
			name = "completed"
		}
		t.Run(name, func(t *testing.T) {
			b := journalBus(t)
			b.cfg.Layouts["old"] = config.Layout{Kind: "dock-grid", Tiles: []string{"w"}}
			b.cfg.Layouts["new"] = config.Layout{Kind: "dock-grid", Tiles: []string{"w"}}
			old, _, err := b.acceptAction(proto.Msg{Type: proto.TypeCtl, Cmd: "layout", Arg: "old", OperationID: "old"}, actionjournal.Reconcile)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.transitionAction(old, actionjournal.Pending, actionjournal.Running, ""); err != nil {
				t.Fatal(err)
			}
			newer := proto.Msg{Type: proto.TypeCtl, Cmd: "layout", Arg: "new", OperationID: "new"}
			if completed {
				if r := b.executeCtl(newer); !r.OK {
					t.Fatal(r.Error)
				}
			} else {
				if _, _, err := b.acceptAction(newer, actionjournal.Reconcile); err != nil {
					t.Fatal(err)
				}
			}
			if err := b.journal.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			b.recoverActions()
			if b.cfg.Layout != "new" {
				t.Fatalf("recovery restored stale desired state: %s", b.cfg.Layout)
			}
			records, err := b.journal.List(context.Background(), "")
			if err != nil || len(records) != 2 || records[0].State != actionjournal.Discarded || records[1].State != actionjournal.Succeeded {
				t.Fatalf("wrong recovery outcomes: %+v %v", records, err)
			}
		})
	}
}

type lostOutcomeModule struct{ store *actionjournal.Store }

func (*lostOutcomeModule) Name() string { return "reno" }
func (*lostOutcomeModule) Poll(context.Context, map[string]any) (module.Data, error) {
	return module.Data{}, nil
}
func (m *lostOutcomeModule) HandleCtl([]string) (any, []module.Effect, error) {
	// The semantic operation succeeded, but its owner can no longer save an outcome.
	m.store.Close()
	return nil, nil, nil
}
func TestControlDoesNotAcknowledgeUncommittedOutcome(t *testing.T) {
	b := journalBus(t)
	b.mods = map[string]module.Module{"reno": &lostOutcomeModule{store: b.journal}}
	response := b.executeCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"next"}, OperationID: "lost-outcome"})
	if response.OK || response.OperationState != string(actionjournal.Uncertain) || response.OperationID != "lost-outcome" || response.Error == "" {
		t.Fatalf("acknowledged unsaved outcome: %+v", response)
	}
}

func TestConcurrentDesiredStateMatchesJournalOrder(t *testing.T) {
	b := journalBus(t)
	for i := 0; i < 24; i++ {
		b.cfg.Layouts[fmt.Sprint(i)] = config.Layout{Kind: "dock-grid", Tiles: []string{"w"}}
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Go(func() {
			<-start
			name := fmt.Sprint(i)
			if r := b.executeCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "layout", Arg: name, OperationID: "layout-" + name}); !r.OK {
				t.Error(r.Error)
			}
		})
	}
	close(start)
	wg.Wait()
	records, err := b.journal.List(context.Background(), "")
	if err != nil || len(records) != 24 {
		t.Fatalf("missing choices: %d %v", len(records), err)
	}
	var last proto.Msg
	if err := json.Unmarshal(records[len(records)-1].Payload, &last); err != nil {
		t.Fatal(err)
	}
	if b.cfg.Layout != last.Arg {
		t.Fatalf("runtime %s contradicts latest accepted choice %s", b.cfg.Layout, last.Arg)
	}
}

type sharedOwnerModule struct {
	lostOutcomeModule
	err error
}

func (*sharedOwnerModule) HandleAct([]string) bool { return false }
func (m *sharedOwnerModule) Act(context.Context, []string, module.Operation) error {
	return m.err
}

// The Reno client reports definite refusals and lost replies differently; the
// row-act journal must keep that distinction, as the control path does.
func TestSharedOwnerRowActKeepsDefiniteFailure(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "bus-owner-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	for _, tc := range []struct {
		name  string
		owner module.Module
		want  actionjournal.State
	}{
		{"accepted", &sharedOwnerModule{}, actionjournal.Dispatched},
		{"refused", &sharedOwnerModule{err: errors.New("Reno owner: Reno action not recognized (failed)")}, actionjournal.Failed},
		{"reply-lost", &sharedOwnerModule{err: fmt.Errorf("Reno operation x: %w", errors.Join(socketio.ErrUncertainDelivery, io.EOF))}, actionjournal.Uncertain},
		{"owner-absent", &renoservice.Client{Socket: filepath.Join(dir, "absent.sock")}, actionjournal.Failed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := journalBus(t)
			b.cfg.Widgets["reno"] = config.Widget{ID: "reno", Render: config.Render{Kind: "native", Module: "reno"}}
			b.reg = NewRegistry(b.cfg)
			b.mods = map[string]module.Module{"reno": tc.owner}
			argv := []string{"reno:next"}
			st, _ := b.reg.Get("reno")
			st.setActs([][]string{argv})
			b.input = make(chan proto.Msg, 1)
			b.input <- proto.Msg{Type: proto.TypeRowAct, Widget: "reno", Argv: argv, OperationID: "row-" + tc.name}
			close(b.input)
			b.inputWorkerContext(context.Background())
			records, err := b.journal.List(context.Background(), "")
			if err != nil || len(records) != 1 || records[0].State != tc.want {
				t.Fatalf("outcome = %+v %v, want %s", records, err, tc.want)
			}
		})
	}
}

type uncertainControlModule struct{ lostOutcomeModule }

func (*uncertainControlModule) HandleCtl([]string) (any, []module.Effect, error) {
	return nil, nil, socketio.ErrUncertainDelivery
}
func TestRemoteControlUnknownOutcomeSurvivesBusJournal(t *testing.T) {
	b := journalBus(t)
	b.mods = map[string]module.Module{"reno": &uncertainControlModule{}}
	response := b.executeCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "reno", Args: []string{"next"}, OperationID: "remote-unknown"})
	records, err := b.journal.List(context.Background(), actionjournal.Uncertain)
	if response.OK || response.OperationState != string(actionjournal.Uncertain) || err != nil || len(records) != 1 || records[0].ID != "remote-unknown" {
		t.Fatalf("uncertainty lost: %+v records=%+v err=%v", response, records, err)
	}
}
