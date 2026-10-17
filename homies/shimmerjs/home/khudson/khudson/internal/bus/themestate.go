package bus

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/shimmerjs/khudson/khudson/internal/atomicfile"
	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
)

// Keep desired theme separate from host palette observations. The intent must
// survive a restart even when no Edge host existed to apply it.
func (b *Bus) persistTheme(theme string) error {
	if theme != "day" && theme != "night" {
		return fmt.Errorf("invalid theme %q", theme)
	}
	if b.opts.Paths.Dir == "" {
		return nil
	}
	return atomicfile.Write(filepath.Join(b.opts.Paths.Dir, "theme-state"), []byte(theme+"\n"), 0600)
}

func (b *Bus) restoreTheme() {
	if b.opts.Paths.Dir == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(b.opts.Paths.Dir, "theme-state"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	theme := strings.TrimSpace(string(data))
	if err == nil && theme != "day" && theme != "night" {
		err = fmt.Errorf("invalid persisted theme %q", theme)
	}
	diagnostic.Default.Observe("theme.restore", err, "preserve file; retain configured default on failure")
	if err != nil {
		return
	}
	b.theme, b.themePending = theme, true
	b.palette = map[string]string{}
	if theme == "night" && b.cfg.Theme != nil {
		b.palette = maps.Clone(b.cfg.Theme.Night.Colors)
	}
}
