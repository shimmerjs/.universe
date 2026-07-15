package rc

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// lsTree mirrors kitty 0.47.4 `ls` output for the fields khudson reads:
// one OS window, one tab, a dock window and a user-var-bound scrape window.
const lsTree = `[{
  "id": 1, "is_active": true, "is_focused": true, "last_focused": true,
  "platform_window_id": 55, "wm_class": "kitty", "wm_name": "khudson-hud",
  "tabs": [{
    "id": 3, "title": "dock", "is_active": true, "is_focused": true, "layout": "stack",
    "active_window_history": [2, 1],
    "windows": [
      {"id": 1, "title": "khudson dock", "pid": 100, "cwd": "/", "cmdline": ["khudson", "dock"],
       "env": {"TERM": "xterm-kitty"}, "is_active": false, "is_focused": false, "is_self": false,
       "lines": 24, "columns": 196, "user_vars": {},
       "foreground_processes": [{"pid": 100, "cwd": "/", "cmdline": ["khudson", "dock"]}]},
      {"id": 2, "title": "btop", "pid": 200, "cwd": "/tmp", "cmdline": ["btop"],
       "env": {}, "is_active": true, "is_focused": true, "is_self": false,
       "lines": 20, "columns": 164, "user_vars": {"khudson_widget": "btop"},
       "foreground_processes": []}
    ]
  }]
}]`

// TestLSShape pins the ls response contract: kitty returns the tree
// serialized INTO a JSON string (dataString unwrap), and the OSWindow/Tab/
// Window field mapping plus the FindWindowByUserVar walk hold against the
// 0.47.4 shape.
func TestLSShape(t *testing.T) {
	data, _ := json.Marshal(lsTree) // string-wrapped, the normal kitty shape
	socket, got := serveOnce(t, response{OK: true, Data: data})
	c := New(socket)
	c.Timeout = 2 * time.Second
	tree, err := c.LS()
	if err != nil {
		t.Fatal(err)
	}
	if env := <-got; env.Cmd != "ls" {
		t.Fatalf("cmd %q, want ls", env.Cmd)
	}
	if len(tree) != 1 || tree[0].ID != 1 || tree[0].WMName != "khudson-hud" {
		t.Fatalf("os window = %+v", tree)
	}
	if tree[0].PlatformWindowID == nil || *tree[0].PlatformWindowID != 55 {
		t.Fatalf("platform_window_id = %v, want 55", tree[0].PlatformWindowID)
	}
	tabs := tree[0].Tabs
	if len(tabs) != 1 || tabs[0].ID != 3 || tabs[0].Layout != "stack" {
		t.Fatalf("tabs = %+v", tabs)
	}
	if len(tabs[0].ActiveWindowHistory) != 2 || tabs[0].ActiveWindowHistory[0] != 2 {
		t.Fatalf("active_window_history = %v", tabs[0].ActiveWindowHistory)
	}
	wins := tabs[0].Windows
	if len(wins) != 2 || wins[1].Lines != 20 || wins[1].Columns != 164 || wins[1].PID != 200 {
		t.Fatalf("windows = %+v", wins)
	}
	if len(wins[0].ForegroundProcesses) != 1 || wins[0].ForegroundProcesses[0].Cmdline[0] != "khudson" {
		t.Fatalf("foreground_processes = %+v", wins[0].ForegroundProcesses)
	}

	w, ok := FindWindowByUserVar(tree, "khudson_widget", "btop")
	if !ok || w.ID != 2 {
		t.Fatalf("FindWindowByUserVar = %+v/%v, want window 2", w, ok)
	}
	if _, ok := FindWindowByUserVar(tree, "khudson_widget", "nope"); ok {
		t.Fatal("FindWindowByUserVar matched a var that is not there")
	}
}

// TestLSBareArray pins the defensive branch: a bare-array data field (not
// string-wrapped) parses too.
func TestLSBareArray(t *testing.T) {
	socket, _ := serveOnce(t, response{OK: true, Data: json.RawMessage(lsTree)})
	c := New(socket)
	c.Timeout = 2 * time.Second
	tree, err := c.LS()
	if err != nil || len(tree) != 1 {
		t.Fatalf("LS = %+v/%v, want the 1-window tree", tree, err)
	}
}

// TestLaunchShape pins the launch contract: the payload rides the
// LaunchOpts JSON keys (omitempty drops unset ones), and the response's
// string-wrapped window id parses.
func TestLaunchShape(t *testing.T) {
	data, _ := json.Marshal("77\n")
	socket, got := serveOnce(t, response{OK: true, Data: data})
	c := New(socket)
	c.Timeout = 2 * time.Second
	id, err := c.Launch(LaunchOpts{
		Args:             []string{"btop"},
		Type:             "os-window",
		OSWindowState:    "minimized",
		OSWindowPosition: "-20000x-20000",
		Var:              []string{"khudson_widget=btop"},
	})
	if err != nil || id != 77 {
		t.Fatalf("Launch = %d/%v, want 77/nil", id, err)
	}
	env := <-got
	if env.Cmd != "launch" {
		t.Fatalf("cmd %q, want launch", env.Cmd)
	}
	payload, _ := json.Marshal(env.Payload)
	for _, want := range []string{
		`"os_window_state":"minimized"`,
		`"os_window_position":"-20000x-20000"`,
		`"var":["khudson_widget=btop"]`,
		`"type":"os-window"`,
	} {
		if !bytes.Contains(payload, []byte(want)) {
			t.Errorf("payload %s missing %s", payload, want)
		}
	}
	if bytes.Contains(payload, []byte(`"cwd"`)) {
		t.Errorf("payload %s carries an unset omitempty key", payload)
	}
}

// TestLaunchResponseTolerance pins the response edges: empty data means no
// id (0, nil); a non-numeric id is a loud error.
func TestLaunchResponseTolerance(t *testing.T) {
	empty, _ := json.Marshal("")
	socket, _ := serveOnce(t, response{OK: true, Data: empty})
	c := New(socket)
	c.Timeout = 2 * time.Second
	if id, err := c.Launch(LaunchOpts{}); id != 0 || err != nil {
		t.Fatalf("Launch(empty data) = %d/%v, want 0/nil", id, err)
	}

	junk, _ := json.Marshal("not-a-number")
	socket, _ = serveOnce(t, response{OK: true, Data: junk})
	c = New(socket)
	c.Timeout = 2 * time.Second
	if _, err := c.Launch(LaunchOpts{}); err == nil {
		t.Fatal("junk launch response did not error")
	}
}
