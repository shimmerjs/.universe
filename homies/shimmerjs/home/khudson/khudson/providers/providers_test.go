package providers

import (
	"testing"
)

func TestCatalogDoesNotConstructSharedJobOwners(t *testing.T) {

	for _, name := range Names() {
		p, err := New(name)
		if err != nil || p.Name() != name {
			t.Fatalf("constructor %s: %v %v", name, p, err)
		}
	}
	if _, err := New("absent"); err == nil {
		t.Fatal("unknown provider accepted")
	}
}
