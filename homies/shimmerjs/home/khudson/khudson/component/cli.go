package component

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
)

// CLI supplies common lifecycle and inspection flags to standalone programs.
func CLI(opts Options, args []string) error {
	p, err := paths.Resolve()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet(opts.Provider.Name(), flag.ContinueOnError)
	dir := fs.String("state-dir", filepath.Join(p.Dir, "components", opts.Provider.Name()), "component state directory")
	params := fs.String("params", "", "provider parameters as a JSON object")
	history := fs.Bool("actions", false, "print retained action outcomes and exit")
	discard := fs.String("discard-action", "", "discard an uncertain operation by ID and exit")
	fs.DurationVar(&opts.Interval, "interval", opts.Interval, "refresh interval (minimum 100ms)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if opts.Interval < 100*time.Millisecond {
		return fmt.Errorf("refresh interval must be at least 100ms")
	}
	if *params != "" {
		if err := json.Unmarshal([]byte(*params), &opts.Widget.Render.Params); err != nil {
			return fmt.Errorf("provider params: %w", err)
		}
	}
	opts.StateDir = *dir
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *history || *discard != "" {
		path := filepath.Join(*dir, "actions.sqlite")
		if _, err := os.Stat(path); err != nil {
			return err
		}
		store, err := actionjournal.Open(path)
		if err != nil {
			return err
		}
		defer store.Close()
		if *discard != "" {
			return store.Transition(ctx, *discard, actionjournal.Uncertain, actionjournal.Discarded, "discarded by user")
		}
		records, err := store.List(ctx, "")
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(records)
	}
	return Run(ctx, opts)
}
