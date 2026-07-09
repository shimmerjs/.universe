package media

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

func TestParseNowPlayingPlaying(t *testing.T) {
	out := "playing\x1fBoards of Canada\x1fRoygbiv\x1fMusic Has the Right to Children\n"
	np, err := parseNowPlaying(out)
	if err != nil {
		t.Fatal(err)
	}
	want := nowPlaying{
		Running: true,
		State:   "playing",
		Artist:  "Boards of Canada",
		Track:   "Roygbiv",
		Album:   "Music Has the Right to Children",
	}
	if np != want {
		t.Fatalf("got %+v, want %+v", np, want)
	}
}

func TestParseNowPlayingPaused(t *testing.T) {
	out := "paused\x1fAphex Twin\x1fXtal\x1fSelected Ambient Works 85-92\n"
	np, err := parseNowPlaying(out)
	if err != nil {
		t.Fatal(err)
	}
	if !np.Running || np.State != "paused" || np.Track != "Xtal" {
		t.Fatalf("got %+v", np)
	}
}

func TestParseNowPlayingEmpty(t *testing.T) {
	for _, out := range []string{"", "\n"} {
		np, err := parseNowPlaying(out)
		if err != nil {
			t.Fatal(err)
		}
		if np.Running {
			t.Fatalf("parseNowPlaying(%q): got %+v, want not running", out, np)
		}
	}
}

func TestParseNowPlayingMalformed(t *testing.T) {
	if _, err := parseNowPlaying("playing\x1fonly two"); err == nil {
		t.Fatal("want error for malformed output")
	}
}

// "||" is legal metadata now that the join rides the unit separator: a track
// title containing it parses cleanly instead of failing the poll.
func TestParseNowPlayingPipesInMetadata(t *testing.T) {
	np, err := parseNowPlaying("playing\x1fArtist\x1fA || B\x1fAlbum\n")
	if err != nil {
		t.Fatal(err)
	}
	if np.Track != "A || B" {
		t.Fatalf("track = %q, want %q", np.Track, "A || B")
	}
}

func TestRenderNowPlaying(t *testing.T) {
	d := renderNowPlaying(nowPlaying{
		Running: true, State: "playing",
		Artist: "Boards of Canada", Track: "Roygbiv", Album: "Music Has the Right to Children",
	})
	if d.Title != "media" {
		t.Fatalf("title %q", d.Title)
	}
	want := []module.Row{
		{Kind: module.RowText, Text: "Roygbiv", Style: module.StyleAccent},
		{Kind: module.RowText, Text: "Boards of Canada"},
		{Kind: module.RowText, Text: "Music Has the Right to Children", Style: module.StyleDim},
		{Kind: module.RowKV, Key: "state", Value: "playing"},
	}
	if len(d.Rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(d.Rows), len(want), d.Rows)
	}
	for i := range want {
		if !reflect.DeepEqual(d.Rows[i], want[i]) {
			t.Fatalf("row %d: got %+v, want %+v", i, d.Rows[i], want[i])
		}
	}
}

func TestRenderNotRunning(t *testing.T) {
	d := renderNowPlaying(nowPlaying{})
	if len(d.Rows) != 1 || d.Rows[0].Text != "spotify not running" || d.Rows[0].Style != module.StyleDim {
		t.Fatalf("got %+v", d.Rows)
	}
}

// Poll over the exec seam: a live-ctx exec failure is loud (TCC/automation
// denials must not render as the calm dim row), a dead ctx returns the ctx
// error, and only exit-0 output decides between the dim not-running row,
// real rows, and a parse error.
func TestPollSeam(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	execErr := errors.New("not authorized to send apple events")
	cases := []struct {
		name    string
		ctx     context.Context
		out     string
		err     error
		wantIs  error // non-nil: errors.Is target on the returned error
		wantErr bool
		wantRow string // first row text on success
		wantSty string
	}{
		{name: "exec error, live ctx", ctx: context.Background(),
			err: execErr, wantIs: execErr, wantErr: true},
		{name: "ctx canceled", ctx: canceled,
			err: errors.New("signal: killed"), wantIs: context.Canceled, wantErr: true},
		{name: "exit 0, empty output", ctx: context.Background(), out: "\n",
			wantRow: "spotify not running", wantSty: module.StyleDim},
		{name: "well-formed output", ctx: context.Background(),
			out:     "playing\x1fAphex Twin\x1fXtal\x1fSelected Ambient Works 85-92\n",
			wantRow: "Xtal", wantSty: module.StyleAccent},
		{name: "malformed output", ctx: context.Background(), out: "playing\x1fonly two",
			wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Mod{run: func(context.Context) ([]byte, error) {
				return []byte(tc.out), tc.err
			}}
			d, err := m.Poll(tc.ctx, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %+v, want error", d)
				}
				if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
					t.Fatalf("err = %v, want errors.Is %v", err, tc.wantIs)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(d.Rows) == 0 || d.Rows[0].Text != tc.wantRow || d.Rows[0].Style != tc.wantSty {
				t.Fatalf("rows = %+v, want first row %q (%s)", d.Rows, tc.wantRow, tc.wantSty)
			}
		})
	}
}
