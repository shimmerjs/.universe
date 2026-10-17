package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/shimmerjs/khudson/khudson/component"
	"github.com/shimmerjs/khudson/khudson/internal/cgdisplay"
	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	"github.com/shimmerjs/khudson/khudson/internal/module/reno"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/renocli"
	"github.com/shimmerjs/khudson/khudson/internal/renoservice"
)

func run(args []string) error {
	if len(args) > 0 && args[0] == "reno" {
		return renocli.Run(args[1:])
	}
	if len(args) > 0 && args[0] == "serve" {
		p, err := paths.Resolve()
		if err != nil {
			return err
		}
		logger, err := diagnostic.OpenFile(filepath.Join(p.Dir, "reno", "owner.jsonl"), 4<<20)
		if err != nil {
			return err
		}
		defer logger.Close()
		log.SetOutput(logger)
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return cgdisplay.WithNotifications(ctx, func(ctx context.Context, changes <-chan struct{}) error {
			cache := &cgdisplay.Cache{Changes: changes}
			return renoservice.Run(ctx, filepath.Join(p.Dir, "reno"), func() renoservice.Backend { return reno.NewWithDisplays(cache.Read) })
		})
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return renocli.Run(args)
	}
	return component.CLI(component.Options{Provider: renoservice.New(), Actions: true, Interval: time.Second, Timeout: 12 * time.Second,
		Widget: component.Widget{ID: "reno", Title: "Recorder", Render: component.Render{Kind: "native", Module: "reno", Params: map[string]any{"view": "recorder"}}},
	}, args)
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "renoman:", err)
		os.Exit(1)
	}
}

func init() { runtime.LockOSThread() }
