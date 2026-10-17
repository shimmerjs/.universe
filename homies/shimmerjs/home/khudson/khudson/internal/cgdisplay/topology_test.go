package cgdisplay

import (
	"errors"
	"testing"
)

func TestTopologyOrderAndDisappearingID(t *testing.T) {
	ids := []uint32{1, 2}
	missing := false
	api := topologyAPI{ids: func() ([]uint32, error) { return append([]uint32(nil), ids...), nil }, bounds: func(id uint32) (int, int, int, int) {
		if missing && id == 2 {
			return 0, 0, 0, 0
		}
		return int(id) * 100, 0, 100, 100
	}}
	a, err := fingerprint(api)
	if err != nil {
		t.Fatal(err)
	}
	ids = []uint32{2, 1}
	b, err := fingerprint(api)
	if err != nil || a != b {
		t.Fatal("enumeration order changed topology")
	}
	missing = true
	if _, err := fingerprint(api); err == nil {
		t.Fatal("removed ID silently accepted")
	}
	ids = nil
	if got, err := fingerprint(api); err != nil || got != "" {
		t.Fatal("valid empty enumeration must be absence")
	}
	failure := &APIError{Boundary: "enumerate", Code: 1002}
	api.ids = func() ([]uint32, error) { return nil, failure }
	if _, err := fingerprint(api); !errors.Is(err, failure) {
		t.Fatal("lost native error")
	}
}
