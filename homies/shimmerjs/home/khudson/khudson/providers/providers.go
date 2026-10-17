// Package providers constructs individual backends without starting the integration bus.
package providers

import (
	"fmt"
	"slices"

	"github.com/shimmerjs/khudson/khudson/internal/module/brightness"
	"github.com/shimmerjs/khudson/khudson/internal/module/cheatsheets"
	"github.com/shimmerjs/khudson/khudson/internal/module/claudesessions"
	"github.com/shimmerjs/khudson/khudson/internal/module/cpumem"
	"github.com/shimmerjs/khudson/khudson/internal/module/demomode"
	"github.com/shimmerjs/khudson/khudson/internal/module/disk"
	"github.com/shimmerjs/khudson/khudson/internal/module/dockmirror"
	"github.com/shimmerjs/khudson/khudson/internal/module/githubprs"
	"github.com/shimmerjs/khudson/khudson/internal/module/kittysessions"
	"github.com/shimmerjs/khudson/khudson/internal/module/logiretch"
	"github.com/shimmerjs/khudson/khudson/internal/module/media"
	"github.com/shimmerjs/khudson/khudson/internal/module/procs"
	"github.com/shimmerjs/khudson/khudson/internal/module/resources"
	"github.com/shimmerjs/khudson/khudson/internal/module/sysmon"
	"github.com/shimmerjs/khudson/khudson/internal/renoservice"
	"github.com/shimmerjs/khudson/khudson/provider"
)

var constructors = map[string]func() module.Module{
	"reno":            func() module.Module { return renoservice.New() },
	"brightness":      func() module.Module { return brightness.Mod{} },
	"cheatsheets":     func() module.Module { return cheatsheets.Mod{} },
	"claude-sessions": func() module.Module { return claudesessions.New() },
	"claude-panel":    func() module.Module { return claudesessions.NewPanel(claudesessions.New()) },
	"cpumem":          func() module.Module { return cpumem.New() },
	"demo-mode":       func() module.Module { return demomode.Mod{} },
	"disk":            func() module.Module { return disk.New() },
	"dock-mirror":     func() module.Module { return dockmirror.New() },
	"github-prs":      func() module.Module { return githubprs.Mod{} },
	"kitty-sessions":  func() module.Module { return kittysessions.Mod{} },
	"logiretch":       func() module.Module { return logiretch.New() },
	"media":           func() module.Module { return media.Mod{} },
	"procs":           func() module.Module { return procs.New() },
	"resources":       func() module.Module { return resources.New(cpumem.New(), disk.New(), procs.New()) },
	"sysmon":          func() module.Module { return sysmon.New() },
}

func Names() []string {
	names := make([]string, 0, len(constructors))
	for name := range constructors {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
func New(name string) (module.Module, error) {
	makeProvider, ok := constructors[name]
	if !ok {
		return nil, fmt.Errorf("unknown standalone provider %q", name)
	}
	return makeProvider(), nil
}
