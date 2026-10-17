package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shimmerjs/khudson/khudson/component"
	"github.com/shimmerjs/khudson/khudson/internal/claudecontrol"
	"github.com/shimmerjs/khudson/khudson/internal/module/claudesessions"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
)

func run(args []string) error {
	v, err := claudecontrol.NewClaudeVerbs()
	if err != nil {
		return err
	}
	if len(args) > 0 && (args[0] == "focus" || args[0] == "resume") {
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("usage: claudeman focus <session> | resume <session> [cwd]")
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if args[0] == "focus" {
			return v.Focus(ctx, args[1])
		}
		cwd := ""
		if len(args) == 3 {
			cwd = args[2]
		}
		return v.Resume(ctx, args[1], cwd)
	}
	p, err := paths.Resolve()
	if err != nil {
		return err
	}
	return component.CLI(component.Options{
		Provider: claudesessions.NewPanel(claudesessions.New()), Interval: time.Second, Timeout: 10 * time.Second, Actions: true,
		Widget: component.Widget{ID: "claude", Title: "Claude sessions", Render: component.Render{Kind: "native", Module: "claude-panel", Params: map[string]any{"dir": p.ClaudeSpool()}}},
		Execute: func(ctx context.Context, argv []string) error {
			if len(argv) == 4 && argv[0] == "khudson" && argv[1] == "claude" && argv[2] == "focus" {
				return v.Focus(ctx, argv[3])
			}
			return fmt.Errorf("unsupported Claude action %q", argv)
		},
	}, args)
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "claudeman:", err)
		os.Exit(1)
	}
}
