package oryx

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shimmerjs/khudson/khudson/internal/paths"
)

// LoadCached returns the last layout FetchLayout wrote through for hashID.
func LoadCached(hashID string) (*Layout, error) {
	p, err := paths.Resolve()
	if err != nil {
		return nil, err
	}
	return loadCache(p.OryxCache(), hashID)
}

func cacheDir() (string, error) {
	p, err := paths.Ensure()
	if err != nil {
		return "", err
	}
	return p.OryxCache(), nil
}

// cacheFile rejects anything but the alphanumeric slugs Oryx issues: the
// hash lands in a filename under the state dir.
func cacheFile(dir, hashID string) (string, error) {
	if hashID == "" {
		return "", fmt.Errorf("oryx: empty layout hash")
	}
	for _, r := range hashID {
		alnum := r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !alnum {
			return "", fmt.Errorf("oryx: invalid layout hash %q", hashID)
		}
	}
	return filepath.Join(dir, hashID+".json"), nil
}

func writeCache(dir, hashID string, l *Layout) error {
	file, err := cacheFile(dir, hashID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("oryx cache: %w", err)
	}
	raw, err := json.Marshal(l)
	if err != nil {
		return fmt.Errorf("oryx cache: encode: %w", err)
	}
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		return fmt.Errorf("oryx cache: %w", err)
	}
	return nil
}

func loadCache(dir, hashID string) (*Layout, error) {
	file, err := cacheFile(dir, hashID)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("oryx cache: %w", err)
	}
	var l Layout
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("oryx cache: decode %s: %w", file, err)
	}
	return &l, nil
}
