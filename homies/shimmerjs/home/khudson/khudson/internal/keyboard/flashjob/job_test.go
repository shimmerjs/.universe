package flashjob

import (
	"context"
	"errors"
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/flash"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/generations"
)

func TestJobOwnedAcrossClientLifetimeAndCannotReplay(t *testing.T) {
	s := Service{Dir: t.TempDir()}
	ctx := context.Background()
	id, err := s.Submit(ctx, "revision", func(id string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	run := func(context.Context, string, func(flash.Event)) (*generations.Record, error) {
		called++
		if _, err := s.Submit(ctx, "other", func(string) error { t.Fatal("second spawn"); return nil }); !errors.Is(err, ErrOwned) {
			t.Fatalf("active job not exclusive: %v", err)
		}
		status, err := s.Inspect(ctx)
		if err != nil || status.State != actionjournal.Running {
			t.Fatalf("active state %+v %v", status, err)
		}
		return &generations.Record{RevisionID: "revision"}, nil
	}
	if err := s.Run(ctx, id, run); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(ctx, id, run); err == nil || called != 1 {
		t.Fatal("completed flash replayed")
	}
	status, err := s.Inspect(ctx)
	if err != nil || status.State != actionjournal.Succeeded {
		t.Fatalf("outcome %+v %v", status, err)
	}
}
func TestMissingWorkerRequiresReviewWithoutReflash(t *testing.T) {
	s := Service{Dir: t.TempDir()}
	ctx := context.Background()
	id, err := s.Submit(ctx, "revision", func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	store, err := s.open()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transition(ctx, id, actionjournal.Pending, actionjournal.Running, ""); err != nil {
		t.Fatal(err)
	}
	store.Close()
	status, err := s.Inspect(ctx)
	if err != nil || status.State != actionjournal.Uncertain {
		t.Fatalf("lost owner %+v %v", status, err)
	}
	if _, err := s.Submit(ctx, "another", func(string) error { return nil }); !errors.Is(err, ErrReview) {
		t.Fatal(err)
	}
	if err := s.Discard(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, "another", func(string) error { return errors.New("spawn failed") }); err == nil {
		t.Fatal("spawn error lost")
	}
	status, err = s.Inspect(ctx)
	if err != nil || status.State != actionjournal.Discarded {
		t.Fatalf("spawn outcome %+v %v", status, err)
	}
}
func TestVerificationFailureNeverReportedAsSafeToRetry(t *testing.T) {
	s := Service{Dir: t.TempDir()}
	ctx := context.Background()
	id, err := s.Submit(ctx, "revision", func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	err = s.Run(ctx, id, func(context.Context, string, func(flash.Event)) (*generations.Record, error) {
		return nil, errors.New("serial never returned")
	})
	if err == nil {
		t.Fatal("lost error")
	}
	status, err := s.Inspect(ctx)
	if err != nil || status.State != actionjournal.Uncertain {
		t.Fatalf("verification outcome %+v %v", status, err)
	}
}
