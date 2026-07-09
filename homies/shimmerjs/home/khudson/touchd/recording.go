package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Recording format: one raw report per line, "<unix ns> <hex bytes>". Plain
// text so captures are inspectable and hand-editable.

type recorder struct {
	mu sync.Mutex
	f  *os.File
}

func newRecorder(path string) (*recorder, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &recorder{f: f}, nil
}

func (r *recorder) write(t int64, raw []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.f, "%d %s\n", t, hex.EncodeToString(raw))
}

func (r *recorder) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

type recordedReport struct {
	t   int64
	raw []byte
}

func loadRecording(path string) ([]recordedReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var reports []recordedReport
	sc := bufio.NewScanner(f)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		ts, rawHex, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("%s:%d: want \"<unix ns> <hex>\"", path, lineNo)
		}
		t, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: timestamp: %w", path, lineNo, err)
		}
		raw, err := hex.DecodeString(rawHex)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: hex: %w", path, lineNo, err)
		}
		reports = append(reports, recordedReport{t: t, raw: raw})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(reports) == 0 {
		return nil, errors.New("empty recording")
	}
	return reports, nil
}

// runReplay serves a recording on the socket in place of hardware: waits for
// the first client, then feeds reports through the same parser at recorded
// pacing, timestamps rebased to now so frame deltas match the capture.
func runReplay(ctx context.Context, b *broadcaster, reports []recordedReport) error {
	fmt.Println("replay: waiting for a client on the socket")
	for b.clientCount() == 0 {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(50 * time.Millisecond):
		}
	}

	start := time.Now()
	base := reports[0].t
	n := 0
	for _, rep := range reports {
		due := start.Add(time.Duration(rep.t - base))
		if d := time.Until(due); d > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(d):
			}
		}
		// parseReport, not parseTouchReport: recordings hold BOTH collections
		// (daemon mode records through the shared emit hook), and on current
		// hardware only mouse 0x07 reports exist -- touch-only parsing would
		// replay every real capture as zero frames
		if f, ok := parseReport(start.UnixNano()+(rep.t-base), rep.raw); ok {
			b.publishJSON(f)
			n++
		}
	}
	fmt.Printf("replay done: %d reports, %d frames\n", len(reports), n)
	// let client writers drain their queues before close hangs them up
	time.Sleep(100 * time.Millisecond)
	return nil
}
