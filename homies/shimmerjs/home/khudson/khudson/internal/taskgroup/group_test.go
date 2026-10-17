package taskgroup

import (
	"context"
	"testing"
)

func TestCloseCancelsJoinsAndRejectsLateCommands(t *testing.T) {
	g := New(context.Background())
	started := make(chan struct{})
	done := make(chan struct{})
	go func() { g.Do(func(ctx context.Context) { close(started); <-ctx.Done() }); close(done) }()
	<-started
	g.Close()
	<-done
	if g.Do(func(context.Context) { t.Fatal("late command ran") }) {
		t.Fatal("late command accepted")
	}
}
