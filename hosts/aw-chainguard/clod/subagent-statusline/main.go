// subagent-statusline renders custom rows for the Claude Code agent panel
// (settings.subagentStatusLine). Called once per refresh tick with
// {columns, tasks[]} on stdin; emits one JSON line {"id","content"} per row.
// Empty content hides a row -- used to drop queued agents that haven't
// started, so big fan-outs show live work only.
//
// Wide panels get a fixed-width cell layout so rows align like a table and
// numerics don't jitter as values grow:
//
//	<glyph> <label [type], padded> . <elapsed>
//
// Every cell has a constant visible width (blank-filled when absent), so each
// row renders to exactly `columns` visible characters with no cross-row
// coordination. Narrow or unknown widths fall back to a compact inline form.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

type payload struct {
	Columns int    `json:"columns"`
	Tasks   []task `json:"tasks"`
}

type task struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Status      string          `json:"status"`
	Description string          `json:"description"`
	Label       string          `json:"label"`
	StartTime   json.RawMessage `json:"startTime"`
	TokenCount  float64         `json:"tokenCount"`
	Cwd         string          `json:"cwd"`
}

type row struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

const (
	reset  = "\x1b[0m"
	dim    = "120;126;138"
	dim2   = "74;82;98"
	cRun   = "34;211;238"
	cOK    = "74;222;128"
	cBad   = "248;113;113"
	cQueue = "148;163;184"
	cLabel = "226;232;240"

	sep = "·" // middle dot

	glyphDone = string(rune(0x2713)) // check mark
	glyphFail = string(rune(0x2717)) // ballot x
	glyphMisc = string(rune(0x2022)) // bullet

	// fixed visible widths for the table layout
	durCellW  = 10 // " . HHMM:SS" (7-char duration)
	minLabelW = 8  // below this, fall back to inline
)

func fg(rgb string) string { return "\x1b[38;2;" + rgb + "m" }

func statusStyle(status string) (glyph, color string) {
	switch status {
	case "running", "in_progress", "active":
		return "⟳", cRun
	case "completed", "done", "success":
		return glyphDone, cOK
	case "failed", "error", "cancelled":
		return glyphFail, cBad
	case "queued", "pending", "waiting":
		return "◌", cQueue
	}
	return glyphMisc, dim
}

func fdur(s int64) string {
	if s < 0 {
		s = 0
	}
	switch {
	case s >= 3600:
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	case s >= 60:
		return fmt.Sprintf("%d:%02d", s/60, s%60)
	}
	return fmt.Sprintf("%ds", s)
}

// startEpoch normalizes startTime to epoch seconds. The wire format is
// undocumented (epoch seconds or millis, number or string, possibly RFC 3339),
// so accept every plausible shape.
func startEpoch(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	if n := new(float64); json.Unmarshal(raw, n) == nil {
		return normEpoch(*n)
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || s == "" {
		return 0, false
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return normEpoch(n)
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.Unix(), true
	}
	return 0, false
}

func normEpoch(n float64) (int64, bool) {
	if n <= 0 {
		return 0, false
	}
	if n > 1e11 { // millis
		return int64(n / 1000), true
	}
	return int64(n), true
}

// visibleWidth counts runes, skipping CSI escape sequences.
func visibleWidth(s string) int {
	w := 0
	inCSI := false
	for _, r := range s {
		switch {
		case inCSI:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inCSI = false
			}
		case r == '\x1b':
			inCSI = true
		default:
			w++
		}
	}
	return w
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 3 {
		return string(r[:max])
	}
	return string(r[:max-3]) + "..."
}

// cells holds one row's pieces, plain text except where noted; assembly
// applies color.
type cells struct {
	glyph string
	color string
	label string
	typ   string // "[researcher]", possibly empty
	dur   string // "1:23", possibly empty
}

func makeCells(t task, now time.Time) (c cells, hide bool) {
	start, hasStart := startEpoch(t.StartTime)

	queued := t.Status == "queued" || t.Status == "pending" || t.Status == "waiting"
	if queued && t.TokenCount == 0 && !hasStart {
		return c, true
	}

	c.glyph, c.color = statusStyle(t.Status)

	c.label = t.Label
	if c.label == "" {
		c.label = t.Name
	}
	if c.label == "" {
		c.label = t.Description
	}
	if c.label == "" {
		c.label = "agent"
	}

	// agent type tag, when it isn't just echoing the label
	if typ := strings.ToLower(t.Type); typ != "" && typ != strings.ToLower(c.label) {
		c.typ = "[" + typ + "]"
	}
	if hasStart {
		c.dur = fdur(now.Unix() - start)
	}
	return c, false
}

// table lays the row out in fixed-width cells: the label cell absorbs all
// remaining width, the right-side groups are constant-width and blank-filled
// when absent, so rows align across the panel.
func (c cells) table(labelW int) string {
	label, typ := c.label, c.typ
	tagW := 0
	if typ != "" {
		tagW = 1 + len([]rune(typ))
	}
	if len([]rune(label))+tagW > labelW {
		if tagW >= labelW-4 {
			typ, tagW = "", 0
		}
		label = truncate(label, labelW-tagW)
	}

	var b strings.Builder
	b.WriteString(fg(c.color) + c.glyph + reset + " ")
	b.WriteString(fg(cLabel) + label + reset)
	if typ != "" {
		b.WriteString(" " + fg(dim2) + typ + reset)
	}
	b.WriteString(strings.Repeat(" ", labelW-len([]rune(label))-tagW))

	if c.dur != "" {
		b.WriteString(" " + fg(dim2) + sep + reset + " " + fg(dim) + fmt.Sprintf("%7s", c.dur) + reset)
	} else {
		b.WriteString(strings.Repeat(" ", durCellW))
	}
	return b.String()
}

// inline is the compact form for narrow or unknown widths: segments appear
// only when present, label truncated as a last resort.
func (c cells) inline(columns int) string {
	var tail strings.Builder
	if c.typ != "" {
		tail.WriteString(" " + fg(dim2) + c.typ + reset)
	}
	if c.dur != "" {
		tail.WriteString(" " + fg(dim2) + sep + reset + " " + fg(dim) + c.dur + reset)
	}

	label := c.label
	if columns > 0 {
		if budget := columns - 2 - visibleWidth(tail.String()); len([]rune(label)) > budget {
			if budget < 4 {
				budget = 4
			}
			label = truncate(label, budget)
		}
	}
	return fg(c.color) + c.glyph + reset + " " + fg(cLabel) + label + reset + tail.String()
}

func render(t task, columns int, now time.Time) (content string, hide bool) {
	c, hide := makeCells(t, now)
	if hide {
		return "", true
	}
	if labelW := columns - 2 - durCellW; columns > 0 && labelW >= minLabelW {
		return c.table(labelW), false
	}
	return c.inline(columns), false
}

func main() {
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}
	// schema discovery: tasks[] wire formats are undocumented; capture raw
	// payloads for inspection when asked
	if p := os.Getenv("CLOD_SL_CAPTURE"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			f.Write(append(in, '\n'))
			f.Close()
		}
	}

	var p payload
	if err := json.Unmarshal(in, &p); err != nil {
		return
	}
	out := json.NewEncoder(os.Stdout)
	now := time.Now()
	for _, t := range p.Tasks {
		if t.ID == "" {
			continue
		}
		content, _ := render(t, p.Columns, now)
		out.Encode(row{ID: t.ID, Content: content})
	}
}
