package resources

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// fakeMod records the params it was polled with and returns canned data.
type fakeMod struct {
	data   module.Data
	err    error
	params map[string]any
}

func (*fakeMod) Name() string { return "fake" }

func (f *fakeMod) Poll(_ context.Context, params map[string]any) (module.Data, error) {
	f.params = params
	return f.data, f.err
}

// fakeProcs is a procs part whose top sampler measured a whole-host
// utilization.
type fakeProcs struct {
	fakeMod
	util    float64
	hasUtil bool
}

func (f *fakeProcs) Utilization() (float64, bool) { return f.util, f.hasUtil }

func TestName(t *testing.T) {
	if got := New(&fakeMod{}, &fakeMod{}, &fakeMod{}).Name(); got != "resources" {
		t.Errorf("Name() = %q, want %q", got, "resources")
	}
}

// TestPollUtilizationPropagation pins the cpu-util plumbing: a measured
// utilization from the procs part reaches cpumem as params["cpu-util"]
// without mutating the caller's params; no measurement (or a plain procs
// module) leaves cpumem's params alone.
func TestPollUtilizationPropagation(t *testing.T) {
	cm := &fakeMod{}
	pr := &fakeProcs{util: 0.42, hasUtil: true}
	m := &Mod{cpumem: cm, disk: &fakeMod{}, procs: pr}
	params := map[string]any{"window": "6h"}
	if _, err := m.Poll(context.Background(), params); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := cm.params["cpu-util"]; got != 0.42 {
		t.Errorf("cpumem params[cpu-util] = %v, want 0.42", got)
	}
	if got := cm.params["window"]; got != "6h" {
		t.Errorf("cpumem params[window] = %v, want 6h (augmented copy keeps params)", got)
	}
	if _, ok := params["cpu-util"]; ok {
		t.Error("caller params mutated")
	}

	pr.hasUtil = false
	if _, err := m.Poll(context.Background(), params); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if _, ok := cm.params["cpu-util"]; ok {
		t.Error("cpu-util injected without a measurement")
	}

	m = &Mod{cpumem: cm, disk: &fakeMod{}, procs: &fakeMod{}}
	if _, err := m.Poll(context.Background(), params); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if _, ok := cm.params["cpu-util"]; ok {
		t.Error("cpu-util injected from a procs part without the accessor")
	}
}

// TestPollCompositionOrder pins the row order the dock renderer relies on:
// resource rows (cpu, mem, then disk volumes), one divider, then process kv rows.
func TestPollCompositionOrder(t *testing.T) {
	m := &Mod{
		cpumem: &fakeMod{data: module.Data{Title: "cpu / mem", Rows: []module.Row{
			{Kind: module.RowResource, Key: "cpu 6h"},
			{Kind: module.RowResource, Key: "mem 6h"},
		}}},
		disk: &fakeMod{data: module.Data{Title: "disk", Rows: []module.Row{
			{Kind: module.RowResource, Key: "/data 6h"},
			{Kind: module.RowResource, Key: "/ 6h"},
		}}},
		procs: &fakeMod{data: module.Data{Title: "procs", Rows: []module.Row{
			{Kind: module.RowKV, Key: "windowserver", Value: "42.0%c 1.2%m"},
			{Kind: module.RowKV, Key: "kernel_task", Value: "12.3%c 0.4%m"},
		}}},
	}

	data, err := m.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if data.Title != "resources" {
		t.Errorf("Title = %q, want %q", data.Title, "resources")
	}
	want := []struct{ kind, key string }{
		{module.RowResource, "cpu 6h"},
		{module.RowResource, "mem 6h"},
		{module.RowResource, "/data 6h"},
		{module.RowResource, "/ 6h"},
		{module.RowDivider, ""},
		{module.RowKV, "windowserver"},
		{module.RowKV, "kernel_task"},
	}
	if len(data.Rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(data.Rows), len(want), data.Rows)
	}
	for i, w := range want {
		if data.Rows[i].Kind != w.kind || data.Rows[i].Key != w.key {
			t.Errorf("rows[%d] = {%s %s}, want {%s %s}", i, data.Rows[i].Kind, data.Rows[i].Key, w.kind, w.key)
		}
	}
}

func TestPollParamPassthrough(t *testing.T) {
	cm := &fakeMod{}
	dk := &fakeMod{}
	pr := &fakeMod{}
	m := &Mod{cpumem: cm, disk: dk, procs: pr}
	params := map[string]any{"volumes": []any{"/data", "/"}, "window": "12h", "top": int64(5)}

	if _, err := m.Poll(context.Background(), params); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	for name, got := range map[string]map[string]any{"cpumem": cm.params, "disk": dk.params, "procs": pr.params} {
		vols, ok := got["volumes"].([]any)
		if !ok || len(vols) != 2 || vols[0] != "/data" || vols[1] != "/" {
			t.Errorf("%s params = %v, want volumes [/data /]", name, got)
		}
		if got["window"] != "12h" || got["top"] != int64(5) {
			t.Errorf("%s params = %v, want window 12h top 5", name, got)
		}
	}
}

func TestPollOnePartFailsDimText(t *testing.T) {
	cpuRows := []module.Row{
		{Kind: module.RowResource, Key: "cpu 6h"},
		{Kind: module.RowResource, Key: "mem 6h"},
	}
	diskRows := []module.Row{{Kind: module.RowResource, Key: "/ 6h"}}
	procRows := []module.Row{{Kind: module.RowKV, Key: "zsh", Value: "0.1%c 0.0%m"}}

	m := &Mod{
		cpumem: &fakeMod{err: errors.New("sysctl vm.loadavg: boom")},
		disk:   &fakeMod{data: module.Data{Rows: diskRows}},
		procs:  &fakeMod{data: module.Data{Rows: procRows}},
	}
	data, err := m.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll should not error when only cpumem fails: %v", err)
	}
	if len(data.Rows) != 4 {
		t.Fatalf("got %d rows, want 4: %+v", len(data.Rows), data.Rows)
	}
	if r := data.Rows[0]; r.Kind != module.RowText || r.Style != module.StyleDim ||
		!strings.Contains(r.Text, "cpu/mem") || !strings.Contains(r.Text, "boom") {
		t.Errorf("failed cpumem row = %+v, want dim text naming cpu/mem and the error", r)
	}
	if data.Rows[1].Key != "/ 6h" || data.Rows[2].Kind != module.RowDivider || data.Rows[3].Key != "zsh" {
		t.Errorf("rows after failed cpumem = %+v, want disk, divider, procs", data.Rows[1:])
	}

	m = &Mod{
		cpumem: &fakeMod{data: module.Data{Rows: cpuRows}},
		disk:   &fakeMod{err: errors.New("statfs: boom")},
		procs:  &fakeMod{data: module.Data{Rows: procRows}},
	}
	data, err = m.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll should not error when only disk fails: %v", err)
	}
	if len(data.Rows) != 5 {
		t.Fatalf("got %d rows, want 5: %+v", len(data.Rows), data.Rows)
	}
	if r := data.Rows[2]; r.Kind != module.RowText || r.Style != module.StyleDim ||
		!strings.Contains(r.Text, "disk") || !strings.Contains(r.Text, "boom") {
		t.Errorf("failed disk row = %+v, want dim text naming disk and the error", r)
	}

	m = &Mod{
		cpumem: &fakeMod{data: module.Data{Rows: cpuRows}},
		disk:   &fakeMod{data: module.Data{Rows: diskRows}},
		procs:  &fakeMod{err: errors.New("ps: boom")},
	}
	data, err = m.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll should not error when only procs fails: %v", err)
	}
	if len(data.Rows) != 5 {
		t.Fatalf("got %d rows, want 5: %+v", len(data.Rows), data.Rows)
	}
	if r := data.Rows[4]; r.Kind != module.RowText || r.Style != module.StyleDim ||
		!strings.Contains(r.Text, "procs") || !strings.Contains(r.Text, "boom") {
		t.Errorf("failed procs row = %+v, want dim text naming procs and the error", r)
	}
	if data.Rows[3].Kind != module.RowDivider {
		t.Errorf("rows[3] = %+v, want divider before the failed procs row", data.Rows[3])
	}
}

func TestPollAllFail(t *testing.T) {
	cmErr := errors.New("cpu boom")
	dkErr := errors.New("disk boom")
	prErr := errors.New("procs boom")
	m := &Mod{cpumem: &fakeMod{err: cmErr}, disk: &fakeMod{err: dkErr}, procs: &fakeMod{err: prErr}}

	_, err := m.Poll(context.Background(), nil)
	if err == nil {
		t.Fatal("Poll should error when every part fails")
	}
	if !errors.Is(err, cmErr) || !errors.Is(err, dkErr) || !errors.Is(err, prErr) {
		t.Errorf("err = %v, want all part errors wrapped", err)
	}
}
