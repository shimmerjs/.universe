// Package githubprs is the open pull requests module: gh search results
// as a compact list. Pure data mapper over the gh CLI's JSON output.
package githubprs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

const (
	defaultSearch = "is:open review-requested:@me"
	defaultLimit  = 10
	maxShown      = 8 // 2 rows per PR; keep within a ~20-row panel
	titleWidth    = 60
)

// Mod implements module.Module.
type Mod struct{}

func (Mod) Name() string { return "github-prs" }

func (Mod) Poll(ctx context.Context, params map[string]any) (module.Data, error) {
	search := defaultSearch
	if s, ok := params["search"].(string); ok && s != "" {
		search = s
	}
	limit := module.IntParam(params, "limit", defaultLimit)

	args := []string{"search", "prs",
		"--json", "title,repository,number,author,updatedAt,url",
		"--limit", strconv.Itoa(limit)}
	args = append(args, strings.Fields(search)...)
	out, err := exec.CommandContext(ctx, "gh", args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return module.Data{}, fmt.Errorf("gh: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return module.Data{}, fmt.Errorf("gh: %w", err)
	}

	prs, err := parsePRs(out)
	if err != nil {
		return module.Data{}, err
	}
	return module.Data{Title: "pull requests", Rows: renderPRs(prs, time.Now())}, nil
}

// pr mirrors the fields requested via --json.
type pr struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	UpdatedAt time.Time `json:"updatedAt"`
	Author    struct {
		Login string `json:"login"`
	} `json:"author"`
	Repository struct {
		Name          string `json:"name"`
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

func (p pr) repoShort() string {
	if p.Repository.Name != "" {
		return p.Repository.Name
	}
	if _, name, ok := strings.Cut(p.Repository.NameWithOwner, "/"); ok {
		return name
	}
	return p.Repository.NameWithOwner
}

func parsePRs(out []byte) ([]pr, error) {
	var prs []pr
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("gh: bad json: %w", err)
	}
	return prs, nil
}

func renderPRs(prs []pr, now time.Time) []module.Row {
	if len(prs) == 0 {
		return []module.Row{{Kind: module.RowText, Text: "no matching PRs", Style: module.StyleDim}}
	}
	shown := prs
	if len(shown) > maxShown {
		shown = shown[:maxShown]
	}
	var rows []module.Row
	for _, p := range shown {
		rows = append(rows,
			module.Text(fmt.Sprintf("#%d %s %s", p.Number, p.repoShort(), truncate(p.Title, titleWidth))),
			module.Row{Kind: module.RowText, Text: p.Author.Login + " " + module.Age(p.UpdatedAt, now), Style: module.StyleDim},
		)
	}
	if n := len(prs) - maxShown; n > 0 {
		rows = append(rows, module.Row{Kind: module.RowText, Text: fmt.Sprintf("+%d more", n), Style: module.StyleDim})
	}
	return rows
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}
