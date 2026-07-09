package rc

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// OSWindow is one entry in the `ls` tree.
type OSWindow struct {
	ID               int    `json:"id"`
	IsActive         bool   `json:"is_active"`
	IsFocused        bool   `json:"is_focused"`
	LastFocused      bool   `json:"last_focused"`
	PlatformWindowID *int64 `json:"platform_window_id"`
	WMClass          string `json:"wm_class"`
	WMName           string `json:"wm_name"`
	Tabs             []Tab  `json:"tabs"`
}

// Tab is one kitty tab inside an OS window.
type Tab struct {
	ID                  int      `json:"id"`
	Title               string   `json:"title"`
	IsActive            bool     `json:"is_active"`
	IsFocused           bool     `json:"is_focused"`
	Layout              string   `json:"layout"`
	Windows             []Window `json:"windows"`
	ActiveWindowHistory []int    `json:"active_window_history"`
}

// Window is one kitty window; UserVars carries the khudson widget binding.
type Window struct {
	ID                  int               `json:"id"`
	Title               string            `json:"title"`
	PID                 int               `json:"pid"`
	CWD                 string            `json:"cwd"`
	Cmdline             []string          `json:"cmdline"`
	Env                 map[string]string `json:"env"`
	IsActive            bool              `json:"is_active"`
	IsFocused           bool              `json:"is_focused"`
	IsSelf              bool              `json:"is_self"`
	Lines               int               `json:"lines"`
	Columns             int               `json:"columns"`
	UserVars            map[string]string `json:"user_vars"`
	ForegroundProcesses []Process         `json:"foreground_processes"`
}

// Process is a foreground process entry under a Window.
type Process struct {
	PID     int      `json:"pid"`
	CWD     string   `json:"cwd"`
	Cmdline []string `json:"cmdline"`
}

// LS returns the full window tree.
func (c *Client) LS() ([]OSWindow, error) {
	data, err := c.call("ls", nil)
	if err != nil {
		return nil, err
	}
	var tree []OSWindow
	// data is normally the tree serialized as a JSON string; accept a bare
	// array too.
	if err := json.Unmarshal(data, &tree); err == nil {
		return tree, nil
	}
	s := dataString(data)
	if err := json.Unmarshal([]byte(s), &tree); err != nil {
		return nil, fmt.Errorf("ls: parse tree: %w", err)
	}
	return tree, nil
}

// FindWindowByUserVar walks an ls tree for the first window whose user var
// key equals value.
func FindWindowByUserVar(tree []OSWindow, key, value string) (Window, bool) {
	for _, osw := range tree {
		for _, tab := range osw.Tabs {
			for _, w := range tab.Windows {
				if w.UserVars[key] == value {
					return w, true
				}
			}
		}
	}
	return Window{}, false
}

// LaunchOpts mirrors the useful subset of the launch payload. Var carries
// user vars ("k=v"), the widget<->window binding.
type LaunchOpts struct {
	Args      []string `json:"args,omitempty"`
	Type      string   `json:"type,omitempty"` // window|tab|os-window|overlay|background
	Cwd       string   `json:"cwd,omitempty"`
	Env       []string `json:"env,omitempty"` // "K=V"
	Title     string   `json:"title,omitempty"`
	Hold      bool     `json:"hold,omitempty"`
	KeepFocus bool     `json:"keep_focus,omitempty"`
	// OSWindowState is normal|fullscreen|maximized|minimized, applied at
	// creation. kitty 0.47.4 has NO hidden state (an unknown payload key
	// is silently ignored and the window comes up visible); minimized is
	// the scrape substrate.
	OSWindowState string `json:"os_window_state,omitempty"`
	// OSWindowPosition is "XxY" at creation. Panel-instance windows cannot
	// miniaturize (no miniaturizable style mask), so scrape windows also
	// get parked far off-screen.
	OSWindowPosition string   `json:"os_window_position,omitempty"`
	Var              []string `json:"var,omitempty"` // user vars "k=v"
	Match            string   `json:"match,omitempty"`
}

// Launch creates a window and returns its kitty window id.
func (c *Client) Launch(opts LaunchOpts) (int, error) {
	data, err := c.call("launch", opts)
	if err != nil {
		return 0, err
	}
	s := dataString(data)
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	id, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("launch: unexpected response %q", s)
	}
	return id, nil
}

// ResizeOSWindow sizes an OS window in cells; how hidden scrape windows get
// cut to the panel region.
func (c *Client) ResizeOSWindow(match string, widthCells, heightCells int) error {
	_, err := c.call("resize-os-window", map[string]any{
		"match":       match,
		"action":      "resize",
		"unit":        "cells",
		"width":       widthCells,
		"height":      heightCells,
		"incremental": false,
	})
	return err
}

// CloseWindow closes the matched window.
func (c *Client) CloseWindow(match string) error {
	_, err := c.call("close-window", map[string]any{"match": match})
	return err
}

// HideOSWindow hides the matched window's OS window entirely (orderOut:
// no screen presence, no Dock tile) -- the scrape substrate's true state.
func (c *Client) HideOSWindow(match string) error {
	_, err := c.call("resize-os-window", map[string]any{
		"match":  match,
		"action": "hide",
	})
	return err
}

// SendBytes injects raw bytes (ESC included) into the matched window's
// input; base64 keeps SGR mouse reports intact on the wire.
func (c *Client) SendBytes(match string, b []byte) error {
	_, err := c.call("send-text", map[string]any{
		"match": match,
		"data":  "base64:" + base64.StdEncoding.EncodeToString(b),
	})
	return err
}

// SGRMouse encodes an SGR mouse report (CSI < btn ; x ; y M/m), 1-based
// cell coordinates, for injection into a scraped TUI via SendBytes.
func SGRMouse(button, x, y int, release bool) []byte {
	suffix := byte('M')
	if release {
		suffix = 'm'
	}
	return fmt.Appendf(nil, "\x1b[<%d;%d;%d%c", button, x, y, suffix)
}

// SendKey sends semantic key events (kitty key names, e.g. "ctrl+a").
func (c *Client) SendKey(match string, keys ...string) error {
	_, err := c.call("send-key", map[string]any{
		"match": match,
		"keys":  keys,
	})
	return err
}

// GetTextOpts selects what GetText captures.
type GetTextOpts struct {
	Match          string `json:"match,omitempty"`
	Extent         string `json:"extent,omitempty"` // screen|all|selection|...; default screen
	ANSI           bool   `json:"ansi,omitempty"`
	Cursor         bool   `json:"cursor,omitempty"`
	WrapMarkers    bool   `json:"wrap_markers,omitempty"`
	ClearSelection bool   `json:"clear_selection,omitempty"`
	Self           bool   `json:"self,omitempty"`
}

// GetText captures window content; ANSI true is the scrape path.
func (c *Client) GetText(opts GetTextOpts) (string, error) {
	if opts.Extent == "" {
		opts.Extent = "screen"
	}
	data, err := c.call("get-text", opts)
	if err != nil {
		return "", err
	}
	return dataString(data), nil
}
