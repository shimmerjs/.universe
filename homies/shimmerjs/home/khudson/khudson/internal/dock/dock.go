// Package dock adapts the reusable surface to the integrated bus and keyboard reader.
package dock

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/usbserial"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/taskgroup"
	"github.com/shimmerjs/khudson/khudson/internal/viewtransport"
	"github.com/shimmerjs/khudson/khudson/surface"
)

type Options = surface.Options

func Run(opts Options) error {
	p, err := paths.Resolve()
	if err != nil {
		return err
	}
	logger, err := diagnostic.OpenFile(filepath.Join(p.Dir, "log", "dock.jsonl"), 1<<20)
	if err != nil {
		return err
	}
	previous := log.Writer()
	log.SetOutput(logger)
	defer func() { log.SetOutput(previous); logger.Close() }()
	var cfg *config.Config
	if opts.ConfigPath == "" {
		cfg, err = config.LoadExample()
	} else {
		cfg, err = config.LoadFile(opts.ConfigPath)
	}
	if err != nil {
		return err
	}
	journal, err := actionjournal.Open(filepath.Join(p.Dir, "actions.sqlite"))
	if err != nil {
		return err
	}
	defer journal.Close()
	session := viewtransport.New(journal)
	defer session.Close()
	loader := &keyboard.Loader{Poller: &usbserial.Poller{TTL: 10 * time.Second}}
	watch := keyboard.Watch(session.Context(), loader, nil)
	defer watch.Close()
	tasks := taskgroup.New(session.Context())
	defer tasks.Close()
	m := surface.New(opts, cfg, session, watch, func(u string) {
		go tasks.Do(func(ctx context.Context) {
			cmd := exec.CommandContext(ctx, "/usr/bin/open", u)
			cmd.WaitDelay = time.Second
			diagnostic.Default.Observe("view.url.open", cmd.Run(), "record outcome")
		})
	})
	_, err = tea.NewProgram(m).Run()
	diagnostic.Default.Observe("terminal.visibility.restore", m.RestoreVisibility(os.Stdout), "restore previous mode")
	return err
}
