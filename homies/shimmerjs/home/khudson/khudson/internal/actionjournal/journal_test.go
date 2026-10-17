package actionjournal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func openTest(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func request(id string, policy Policy) Request {
	return Request{ID: id, Kind: "action", Source: "test", Policy: policy, Payload: json.RawMessage(`{"target":"test"}`)}
}
func TestRestartDecisionsAndDuplicateAcceptance(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "actions.sqlite")
	s := openTest(t, path)
	for _, r := range []Request{request("queued", DiscardOnRestart), request("running", DiscardOnRestart), request("desired", Reconcile), request("cleanup", Review)} {
		if _, fresh, err := s.Accept(ctx, r); err != nil || !fresh {
			t.Fatalf("accept: %v %v", fresh, err)
		}
	}
	if err := s.Transition(ctx, "running", Pending, Running, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Transition(ctx, "desired", Pending, Running, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]State{"queued": Discarded, "running": Uncertain, "desired": Pending, "cleanup": Uncertain}
	for _, r := range rows {
		if r.State != want[r.ID] {
			t.Fatalf("%s = %s", r.ID, r.State)
		}
	}
	old, fresh, err := s.Accept(ctx, request("running", DiscardOnRestart))
	if err != nil || fresh || old.State != Uncertain {
		t.Fatalf("duplicate accepted: %+v %v %v", old, fresh, err)
	}
	changed := request("running", DiscardOnRestart)
	changed.Payload = json.RawMessage(`{"target":"other"}`)
	if _, _, err = s.Accept(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting id: %v", err)
	}
	if err = s.Transition(ctx, "running", Uncertain, Discarded, "reviewed"); err != nil {
		t.Fatal(err)
	}
	if err = s.Transition(ctx, "running", Discarded, Running, ""); !errors.Is(err, ErrTransition) {
		t.Fatalf("terminal replay: %v", err)
	}
}
func TestInvalidCorruptAndFutureJournalsArePreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.sqlite")
	original := []byte("not a sqlite database")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("corruption accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatal("corrupt evidence replaced")
	}
	future := filepath.Join(t.TempDir(), "future.sqlite")
	s := openTest(t, future)
	if _, err = s.db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err = Open(future); err == nil {
		t.Fatal("future schema accepted")
	}
}
func TestWriteFailureAndDurabilitySettings(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "actions.sqlite"))
	ctx := context.Background()
	var mode string
	var synchronous int
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" || synchronous != 2 {
		t.Fatalf("durability = %s/%d", mode, synchronous)
	}
	if _, err := s.db.Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := s.Accept(ctx, request("denied", DiscardOnRestart)); err == nil || fresh {
		t.Fatal("write failure reported acceptance")
	}
}
func TestOnlyOneWorkerClaimsPendingOperation(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "actions.sqlite"))
	ctx := context.Background()
	if _, _, err := s.Accept(ctx, request("one", Review)); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- s.Transition(ctx, "one", Pending, Running, "") }()
	}
	a, b := <-results, <-results
	if !((a == nil && errors.Is(b, ErrTransition)) || (b == nil && errors.Is(a, ErrTransition))) {
		t.Fatalf("claims = %v, %v", a, b)
	}
}

func TestAbruptExitRetainsAcceptedAndRunningRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash.sqlite")
	cmd := exec.Command(os.Args[0], "-test.run=^TestJournalCrashHelper$")
	cmd.Env = append(os.Environ(), "KHUDSON_TEST_JOURNAL_CRASH="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v %s", err, output)
	}
	s := openTest(t, path)
	if err := s.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].State != Discarded || rows[1].State != Uncertain {
		t.Fatalf("crash recovery = %+v", rows)
	}
}
func TestJournalCrashHelper(t *testing.T) {
	path := os.Getenv("KHUDSON_TEST_JOURNAL_CRASH")
	if path == "" {
		return
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"accepted", "running"} {
		if _, _, err = s.Accept(ctx, request(id, DiscardOnRestart)); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Transition(ctx, "running", Pending, Running, ""); err != nil {
		t.Fatal(err)
	}
	os.Exit(0) // no database close/checkpoint or deferred teardown
}

func TestReplacementConnectionsRetainDurabilitySettings(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "actions.sqlite"))
	// Force every query onto a newly opened native connection.
	s.db.SetMaxIdleConns(0)
	for _, pragma := range []string{"fullfsync", "checkpoint_fullfsync", "synchronous"} {
		var value int
		if err := s.db.QueryRow("PRAGMA " + pragma).Scan(&value); err != nil {
			t.Fatal(err)
		}
		want := 1
		if pragma == "synchronous" {
			want = 2
		}
		if value != want {
			t.Fatalf("replacement connection %s=%d, want %d", pragma, value, want)
		}
	}
}

func TestPruningKeepsSupersedingOutcome(t *testing.T) {
	store := openTest(t, filepath.Join(t.TempDir(), "journal.sqlite"))
	ctx := context.Background()
	old := request("old", Reconcile)
	if _, _, err := store.Accept(ctx, old); err != nil {
		t.Fatal(err)
	}
	newer := request("new", Reconcile)
	if _, _, err := store.Accept(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, "new", Pending, Running, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, "new", Running, Succeeded, ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1030; i++ {
		r := request("unrelated-"+strconv.Itoa(i), DiscardOnRestart)
		r.Source = "different-owner"
		if _, _, err := store.Accept(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := store.Transition(ctx, r.ID, Pending, Discarded, ""); err != nil {
			t.Fatal(err)
		}
	}
	records, err := store.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range records {
		if r.ID == "new" {
			found = true
		}
	}
	if !found {
		t.Fatal("pruned the evidence that old desired state was superseded")
	}
	if err := store.Transition(ctx, "old", Pending, Discarded, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Accept(ctx, request("sweep", DiscardOnRestart)); err != nil {
		t.Fatal(err)
	}
	records, err = store.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if r.ID == "new" {
			t.Fatal("retained outcome after reconciliation was resolved")
		}
	}
}
