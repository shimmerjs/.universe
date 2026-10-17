package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestInputDiagnosticsSeparatesMotionFromKeys(t *testing.T) {
	out := observeInput(func(state, event int) (uint32, float64) {
		if event == 5 {
			return 100, 0
		}
		return 2, 60
	})
	for _, source := range out {
		if source["motion"].Count != 100 || source["key_down"].IdleSeconds != 60 || source["left_down"].IdleSeconds != 60 {
			t.Fatal(out)
		}
	}
}
func TestInputDiagnosticsInvalidAgeRemainsSerializable(t *testing.T) {
	out := observeInput(func(int, int) (uint32, float64) { return 1, math.Inf(1) })
	if _, err := json.Marshal(out); err != nil {
		t.Fatal(err)
	}
	if out["session"]["key_down"].Error == "" {
		t.Fatal("lost invalid age")
	}
}
func TestInputJournalBoundedAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.jsonl")
	for range 100 {
		if err := (boundedJournal{path: path, limit: 32}).append([]byte("{\"n\":1}\n")); err != nil {
			t.Fatal(err)
		}
	}
	files, err := filepath.Glob(path + "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("files=%v", files)
	}
	for _, p := range files {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > 32 || info.Mode().Perm() != 0600 {
			t.Fatal(info)
		}
	}
}
func TestInputDiagnosticsStopsAndSurfacesWriteFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		writes := 0
		err := runInputDiagnostics(ctx, time.Second, newInputRate(), func(int, int) (uint32, float64) { return 0, 0 }, func([]byte) error {
			writes++
			if writes == 3 {
				cancel()
			}
			return nil
		})
		if err != nil || writes != 3 {
			t.Fatalf("writes=%d err=%v", writes, err)
		}
		failure := errors.New("disk full")
		err = runInputDiagnostics(context.Background(), time.Second, newInputRate(), func(int, int) (uint32, float64) { return 0, 0 }, func([]byte) error { return failure })
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
	})
}

func TestInputRecorderRetriesWriteFailureWithoutBusyLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		started := time.Now()
		calls := 0
		superviseInputDiagnostics(ctx, func() error {
			calls++
			if calls < 3 {
				return errors.New("full")
			}
			cancel()
			return nil
		})
		if calls != 3 || time.Since(started) != 2*time.Minute {
			t.Fatalf("calls=%d elapsed=%s", calls, time.Since(started))
		}
	})
}
