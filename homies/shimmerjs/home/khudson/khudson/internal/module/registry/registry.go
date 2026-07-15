// Package registry assembles the native modules compiled into khudson. New
// modules: implement module.Module in a sibling package, add one entry
// here, and extend the schema's #Module vocabulary.
package registry

import (
	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/module/brightness"
	"github.com/shimmerjs/khudson/khudson/internal/module/cheatsheets"
	"github.com/shimmerjs/khudson/khudson/internal/module/claudesessions"
	"github.com/shimmerjs/khudson/khudson/internal/module/cpumem"
	"github.com/shimmerjs/khudson/khudson/internal/module/demomode"
	"github.com/shimmerjs/khudson/khudson/internal/module/disk"
	"github.com/shimmerjs/khudson/khudson/internal/module/dockmirror"
	"github.com/shimmerjs/khudson/khudson/internal/module/githubprs"
	"github.com/shimmerjs/khudson/khudson/internal/module/kittysessions"
	"github.com/shimmerjs/khudson/khudson/internal/module/media"
	"github.com/shimmerjs/khudson/khudson/internal/module/procs"
	"github.com/shimmerjs/khudson/khudson/internal/module/resources"
	"github.com/shimmerjs/khudson/khudson/internal/module/sysmon"
)

// All returns every compiled-in module keyed by schema name.
func All() map[string]module.Module {
	// resources composes the same cpumem/disk/procs singletons registered
	// standalone, so history rings and the per-poll execs are shared.
	cm := cpumem.New()
	dk := disk.New()
	pr := procs.New()
	// claude-panel gets its OWN Mod instance: the start cache evicts
	// entries outside the caller's window, and the two widgets poll with
	// different windows -- sharing would thrash the cache (repeated 64KB
	// head re-reads). Order stays identical either way: transcript heads
	// are immutable.
	mods := []module.Module{
		brightness.Mod{},
		cheatsheets.Mod{},
		claudesessions.New(),
		claudesessions.NewPanel(claudesessions.New()),
		cm,
		demomode.Mod{},
		dk,
		dockmirror.New(),
		githubprs.Mod{},
		kittysessions.Mod{},
		media.Mod{},
		pr,
		resources.New(cm, dk, pr),
		sysmon.New(),
	}
	out := make(map[string]module.Module, len(mods))
	for _, m := range mods {
		out[m.Name()] = m
	}
	return out
}
