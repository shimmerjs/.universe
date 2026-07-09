package githubprs

import (
	"fmt"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

const fixtureTwo = `[
  {
    "author": {"id": "MDQ6VXNlcjU4MzIzMQ==", "is_bot": false, "login": "octocat", "type": "User", "url": "https://github.com/octocat"},
    "number": 42,
    "repository": {"name": "hello-world", "nameWithOwner": "octo-org/hello-world"},
    "title": "Fix the frobnicator so it stops dropping the third widget on every other run",
    "updatedAt": "2026-07-03T09:00:00Z",
    "url": "https://github.com/octo-org/hello-world/pull/42"
  },
  {
    "author": {"id": "MDQ6VXNlcjE=", "is_bot": true, "login": "dependabot", "type": "Bot", "url": "https://github.com/apps/dependabot"},
    "number": 7,
    "repository": {"name": "khudson", "nameWithOwner": "shimmerjs/khudson"},
    "title": "Bump golang.org/x/net",
    "updatedAt": "2026-07-01T12:00:00Z",
    "url": "https://github.com/shimmerjs/khudson/pull/7"
  }
]`

var now = time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)

func TestParseAndRender(t *testing.T) {
	prs, err := parsePRs([]byte(fixtureTwo))
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 2 {
		t.Fatalf("parsed %d prs, want 2", len(prs))
	}
	rows := renderPRs(prs, now)
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4", len(rows))
	}
	want0 := "#42 hello-world Fix the frobnicator so it stops dropping the third widget..."
	if rows[0].Text != want0 {
		t.Errorf("row 0 = %q, want %q", rows[0].Text, want0)
	}
	if n := len([]rune("Fix the frobnicator so it stops dropping the third widget...")); n != titleWidth {
		t.Errorf("truncated title is %d runes, want %d", n, titleWidth)
	}
	if rows[1].Text != "octocat 3h" || rows[1].Style != module.StyleDim {
		t.Errorf("row 1 = %+v, want dim 'octocat 3h'", rows[1])
	}
	if rows[2].Text != "#7 khudson Bump golang.org/x/net" {
		t.Errorf("row 2 = %q", rows[2].Text)
	}
	if rows[3].Text != "dependabot 2d" || rows[3].Style != module.StyleDim {
		t.Errorf("row 3 = %+v, want dim 'dependabot 2d'", rows[3])
	}
}

func TestRenderEmpty(t *testing.T) {
	prs, err := parsePRs([]byte("[]"))
	if err != nil {
		t.Fatal(err)
	}
	rows := renderPRs(prs, now)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Text != "no matching PRs" || rows[0].Style != module.StyleDim {
		t.Errorf("row = %+v, want dim 'no matching PRs'", rows[0])
	}
}

func TestRenderCapsList(t *testing.T) {
	prs := make([]pr, maxShown+4)
	for i := range prs {
		prs[i].Number = i + 1
		prs[i].Title = fmt.Sprintf("pr %d", i+1)
		prs[i].Author.Login = "octocat"
		prs[i].Repository.Name = "hello-world"
		prs[i].UpdatedAt = now.Add(-time.Hour)
	}
	rows := renderPRs(prs, now)
	if want := maxShown*2 + 1; len(rows) != want {
		t.Fatalf("got %d rows, want %d", len(rows), want)
	}
	last := rows[len(rows)-1]
	if last.Text != "+4 more" || last.Style != module.StyleDim {
		t.Errorf("last row = %+v, want dim '+4 more'", last)
	}
}

func TestParseBadJSON(t *testing.T) {
	if _, err := parsePRs([]byte("gh: To get started with GitHub CLI, please run: gh auth login")); err == nil {
		t.Fatal("want error for non-json output")
	}
}
