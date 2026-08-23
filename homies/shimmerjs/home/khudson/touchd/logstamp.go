package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"syscall"
	"time"
)

// stampFormat matches khudson's log.LstdFlags, so magicbusd's log and the
// bus/dock logs sort and grep the same way.
const stampFormat = "2006/01/02 15:04:05"

// stampOutput points fd 1 and 2 at a pipe whose reader prefixes every line
// with a timestamp before handing it to the real stdout.
//
// Redirecting the descriptor rather than editing call sites is deliberate.
// magicbusd's diagnostics are spread over ~65 fmt.Print calls, hidapi writes
// to stderr from C, and launchd's log file dates none of it -- so the log of
// the process that owns every HID device on the machine could not be lined up
// against the unified log at all. Descriptor-level catches the lines this
// package does not own, and the ones it grows later.
//
// Daemon path only: the CLI modes print to a terminal where a stamp is noise,
// and -spike's frame dump is data, not log.
func stampOutput() error {
	saved, err := syscall.Dup(1)
	if err != nil {
		return fmt.Errorf("dup stdout: %w", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		syscall.Close(saved)
		return fmt.Errorf("log pipe: %w", err)
	}
	for _, fd := range []int{1, 2} {
		if err := syscall.Dup2(int(w.Fd()), fd); err != nil {
			syscall.Close(saved)
			r.Close()
			w.Close()
			return fmt.Errorf("redirect fd %d: %w", fd, err)
		}
	}
	// both descriptors now hold the write end; this handle is surplus
	w.Close()
	go stampLines(r, os.NewFile(uintptr(saved), "stdout"))
	return nil
}

// stampLines copies src to dst a line at a time, dating each one. It must
// never return while the daemon runs: the pipe buffer fills behind it and the
// next write blocks the writer, so a partial read or a long line is copied
// through rather than treated as a reason to stop.
func stampLines(src io.Reader, dst io.Writer) {
	br := bufio.NewReader(src)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			fmt.Fprintf(dst, "%s %s", time.Now().Format(stampFormat), line)
			if line[len(line)-1] != '\n' {
				fmt.Fprintln(dst)
			}
		}
		if err != nil {
			return
		}
	}
}

// logStart stamps one line at daemon start, matching khudson's own services
// so a restart is visible in the log rather than inferred from a gap.
func logStart() {
	version := "unknown"
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		version = bi.Main.Version
	}
	fmt.Printf("magicbusd: start pid=%d version=%s\n", os.Getpid(), version)
}
