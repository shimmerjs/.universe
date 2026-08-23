package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

var stampRE = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)

// Every line out of the daemon carries a date, including a trailing fragment
// the writer never terminated -- an unstamped line is one that cannot be
// lined up against the unified log, which is the whole point.
func TestStampLines(t *testing.T) {
	var out bytes.Buffer
	stampLines(strings.NewReader("moonlander open (shared)\nlogiretch: battery 55%\nno trailing newline"), &out)

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	want := []string{
		"moonlander open (shared)",
		"logiretch: battery 55%",
		"no trailing newline",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), out.String())
	}
	for i, line := range lines {
		if !stampRE.MatchString(line) {
			t.Errorf("line %d not stamped: %q", i, line)
		}
		if got := stampRE.ReplaceAllString(line, ""); got != want[i] {
			t.Errorf("line %d = %q, want %q", i, got, want[i])
		}
	}
}

// An empty stream must not emit a bare timestamp.
func TestStampLinesEmpty(t *testing.T) {
	var out bytes.Buffer
	stampLines(strings.NewReader(""), &out)
	if out.Len() != 0 {
		t.Fatalf("empty input produced %q", out.String())
	}
}

// The descriptor surgery itself, which the formatter test cannot reach:
// stampOutput rewires process-global fds, so it runs in a re-exec'd child.
// The raw fd-2 write stands for hidapi's C-side stderr -- the case the whole
// descriptor-level approach exists to cover.
func TestStampOutputRedirectsFDs(t *testing.T) {
	if os.Getenv("TOUCHD_STAMP_CHILD") != "" {
		if err := stampOutput(); err != nil {
			fmt.Fprintln(os.Stderr, "stampOutput:", err)
			os.Exit(1)
		}
		fmt.Println("MARK via stdout")
		fmt.Fprintln(os.Stderr, "MARK via stderr")
		syscall.Write(2, []byte("MARK via raw fd 2\n"))
		// the stamper drains asynchronously; give it the pipe before exit
		time.Sleep(200 * time.Millisecond)
		os.Exit(0)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestStampOutputRedirectsFDs$")
	cmd.Env = append(os.Environ(), "TOUCHD_STAMP_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
	marks := 0
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "MARK ") {
			continue
		}
		marks++
		if !stampRE.MatchString(line) {
			t.Errorf("unstamped line reached the log: %q", line)
		}
	}
	if marks != 3 {
		t.Fatalf("saw %d marked lines, want 3:\n%s", marks, out)
	}
}
