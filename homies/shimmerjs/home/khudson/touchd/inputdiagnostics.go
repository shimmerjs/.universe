package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"time"
)

// Counts and idle ages only: no key values, pointer positions or event taps.
type eventObservation struct {
	Count       uint32  `json:"count"`
	IdleSeconds float64 `json:"idle_s"`
	Error       string  `json:"error,omitempty"`
}
type inputObservation struct {
	Version        int                                    `json:"version"`
	Time           time.Time                              `json:"time"`
	PID            int                                    `json:"pid"`
	ElapsedSeconds float64                                `json:"elapsed_s"`
	Reports        int64                                  `json:"hid_reports"`
	Sources        map[string]map[string]eventObservation `json:"sources"`
}

type eventProbe func(state, event int) (uint32, float64)

func observeInput(probe eventProbe) map[string]map[string]eventObservation {
	out := make(map[string]map[string]eventObservation)
	// CGEventSourceStateID and CGEventType values from CGEventTypes.h.
	for name, state := range map[string]int{"hid": 1, "session": 0} {
		events := make(map[string]eventObservation)
		for kind, typ := range map[string]int{"key_down": 10, "key_up": 11, "modifiers": 12, "left_down": 1, "left_up": 2, "right_down": 3, "right_up": 4, "motion": 5, "left_drag": 6, "scroll": 22} {
			count, idle := probe(state, typ)
			e := eventObservation{Count: count, IdleSeconds: idle}
			if math.IsNaN(idle) || math.IsInf(idle, 0) || idle < 0 {
				e.Error = "invalid_idle_age:" + strconv.FormatFloat(idle, 'g', -1, 64)
				e.IdleSeconds = 0
			}
			events[kind] = e
		}
		out[name] = events
	}
	return out
}

// Four files of at most 4 MiB. A single owner appends and rotates, including
// across restarts. The supervisor retries write/rotation failures.
type boundedJournal struct {
	path  string
	limit int64
}

func (j boundedJournal) append(data []byte) error {
	if int64(len(data)) > j.limit {
		return fmt.Errorf("record exceeds journal limit")
	}
	info, err := os.Stat(j.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if info != nil && info.Size()+int64(len(data)) > j.limit {
		if err := os.Remove(j.path + ".3"); err != nil && !os.IsNotExist(err) {
			return err
		}
		for i := 2; i >= 0; i-- {
			from := j.path
			if i > 0 {
				from += "." + strconv.Itoa(i)
			}
			if err := os.Rename(from, j.path+"."+strconv.Itoa(i+1)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, err := f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return closeErr
}

func runInputDiagnostics(ctx context.Context, every time.Duration, rate *inputRate, probe eventProbe, appendRecord func([]byte) error) error {
	previous := time.Now()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		now := time.Now()
		record := inputObservation{Version: 1, Time: now.UTC(), PID: os.Getpid(), ElapsedSeconds: now.Sub(previous).Seconds(), Reports: rate.total(), Sources: observeInput(probe)}
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := appendRecord(append(data, '\n')); err != nil {
			return err
		}
		previous = now
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func superviseInputDiagnostics(ctx context.Context, run func() error) {
	for ctx.Err() == nil {
		if err := run(); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}
