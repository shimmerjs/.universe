package oryx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/getlayout.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDecodeFixture(t *testing.T) {
	l, err := decodeResponse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if l.HashID != "TESTr" || l.Geometry != "moonlander" || l.Title != "khudson test" {
		t.Fatalf("layout header decoded wrong: %+v", l)
	}
	if l.RevisionID != "REVAB" || l.QmkVersion != "24.0" || l.Model != "mk1" {
		t.Fatalf("revision header decoded wrong: %+v", l)
	}
	if len(l.Layers) != 2 {
		t.Fatalf("want 2 layers, got %d", len(l.Layers))
	}
	base, nav := l.Layers[0], l.Layers[1]
	if base.Title != "Base" || base.Position != 0 || nav.Title != "Nav" || nav.Position != 1 {
		t.Fatalf("layer headers decoded wrong: %+v / %+v", base, nav)
	}

	k := base.Keys[0]
	if k.Tap == nil || k.Tap.Code != "KC_A" {
		t.Fatalf("tap decoded wrong: %+v", k.Tap)
	}
	if k.Hold == nil || k.Hold.Code != "MO" || k.Hold.Layer == nil || *k.Hold.Layer != 1 {
		t.Fatalf("MO hold must keep its layer: %+v", k.Hold)
	}

	k = base.Keys[1]
	if k.GlowColor != "#000EFF" || k.TappingTerm != 220 {
		t.Fatalf("glowColor/tappingTerm decoded wrong: %+v", k)
	}
	m := k.Tap.Modifiers
	if m == nil || !m.LeftCtrl || !m.LeftShift || m.RightAlt {
		t.Fatalf("modifiers decoded wrong: %+v", m)
	}

	k = base.Keys[2]
	if k.CustomLabel != "esc" || k.DoubleTap == nil || k.DoubleTap.Code != "KC_GRAVE" {
		t.Fatalf("customLabel/doubleTap decoded wrong: %+v", k)
	}

	// layer 0 is a real target: TO's layer must survive as *0, not nil
	k = nav.Keys[0]
	if k.Tap == nil || k.Tap.Code != "TO" || k.Tap.Layer == nil || *k.Tap.Layer != 0 {
		t.Fatalf("TO layer 0 decoded wrong: %+v", k.Tap)
	}
	if nav.Keys[1].Tap != nil {
		t.Fatalf("transparent key must decode to nil tap: %+v", nav.Keys[1])
	}

	if len(l.Combos) != 1 {
		t.Fatalf("want 1 combo, got %d", len(l.Combos))
	}
	c := l.Combos[0]
	if !reflect.DeepEqual(c.KeyIndices, []int{0, 2}) || c.LayerIdx != 0 || c.Trigger == nil || c.Trigger.Code != "KC_ENTER" {
		t.Fatalf("combo decoded wrong: %+v", c)
	}
}

func TestDecodeGraphQLError(t *testing.T) {
	_, err := decodeResponse([]byte(`{"errors":[{"message":"Field must have selections"}]}`))
	if err == nil || !strings.Contains(err.Error(), "Field must have selections") {
		t.Fatalf("graphql errors must surface: %v", err)
	}
}

func TestDecodeLayoutNotFound(t *testing.T) {
	_, err := decodeResponse([]byte(`{"data":{"layout":null}}`))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("null layout must error: %v", err)
	}
}

func TestFetchLayout(t *testing.T) {
	var gotVars map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		if !strings.Contains(req.Query, "combos { keyIndices layerIdx trigger }") {
			t.Errorf("query lost the combos subselection: %s", req.Query)
		}
		gotVars = req.Variables
		w.Write(fixture(t))
	}))
	defer srv.Close()

	l, err := fetchLayout(t.Context(), srv.Client(), srv.URL, "TESTr", RevisionLatest)
	if err != nil {
		t.Fatal(err)
	}
	if l.HashID != "TESTr" || len(l.Layers) != 2 {
		t.Fatalf("fetched layout decoded wrong: %+v", l)
	}
	want := map[string]string{"hashId": "TESTr", "geometry": "moonlander", "revisionId": "latest"}
	if !reflect.DeepEqual(gotVars, want) {
		t.Fatalf("variables %v, want %v", gotVars, want)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	l, err := decodeResponse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := writeCache(dir, "TESTr", l); err != nil {
		t.Fatal(err)
	}
	got, err := loadCache(dir, "TESTr")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, l) {
		t.Fatalf("round trip mismatch:\ngot  %+v\nwant %+v", got, l)
	}
}

func TestCacheMissing(t *testing.T) {
	if _, err := loadCache(t.TempDir(), "TESTr"); err == nil {
		t.Fatal("missing cache entry must error")
	}
}

func TestCacheRejectsBadHash(t *testing.T) {
	for _, h := range []string{"", "../evil", "a/b", "a.b"} {
		if err := writeCache(t.TempDir(), h, &Layout{}); err == nil {
			t.Fatalf("hash %q must be rejected", h)
		}
	}
}
