package demomode

import (
	"context"
	"reflect"
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

func TestPollDeterministic(t *testing.T) {
	params := map[string]any{"seed": 7}
	a, err := Mod{}.Poll(context.Background(), params)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	b, err := Mod{}.Poll(context.Background(), params)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("same seed, different data:\n%+v\n%+v", a, b)
	}
}

func TestPollSeedsDiffer(t *testing.T) {
	a, err := Mod{}.Poll(context.Background(), map[string]any{"seed": 1})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	b, err := Mod{}.Poll(context.Background(), map[string]any{"seed": 2})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if reflect.DeepEqual(a, b) {
		t.Errorf("different seeds, identical data: %+v", a)
	}
}

func TestPollZeroSeedGauges(t *testing.T) {
	data, err := Mod{}.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	want := [3]float64{0.25, 0.5, 0.9}
	var got []float64
	for _, r := range data.Rows {
		if r.Kind == module.RowGauge {
			got = append(got, r.Frac)
		}
	}
	if len(got) != 3 {
		t.Fatalf("gauge rows = %d, want 3", len(got))
	}
	for i, f := range got {
		if f != want[i] {
			t.Errorf("gauge %d frac = %v, want %v", i, f, want[i])
		}
	}
}

func TestSeedFrom(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		want   int64
	}{
		{"nil", nil, 0},
		{"missing", map[string]any{}, 0},
		{"int", map[string]any{"seed": 42}, 42},
		{"int64", map[string]any{"seed": int64(-3)}, -3},
		{"float64", map[string]any{"seed": float64(9)}, 9},
		{"string", map[string]any{"seed": "9"}, 0},
	}
	for _, c := range cases {
		if got := seedFrom(c.params); got != c.want {
			t.Errorf("%s: seedFrom = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestGaugeFracs(t *testing.T) {
	if got := gaugeFracs(0); got != [3]float64{0.25, 0.5, 0.9} {
		t.Errorf("seed 0: %v", got)
	}
	if gaugeFracs(5) != gaugeFracs(5) {
		t.Error("same seed produced different fracs")
	}
	if gaugeFracs(1) == gaugeFracs(2) {
		t.Error("seeds 1 and 2 produced identical fracs")
	}
	for _, seed := range []int64{-100, -1, 0, 1, 3, 19, 12345} {
		for i, f := range gaugeFracs(seed) {
			if f < 0.05 || f > 1 {
				t.Errorf("seed %d gauge %d frac %v out of [0.05, 1]", seed, i, f)
			}
		}
	}
}
