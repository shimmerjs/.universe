package generations

import (
	"path/filepath"
	"testing"
	"time"
)

func rec(ts string, rev string) Record {
	t, _ := time.Parse(time.RFC3339, ts)
	return Record{FlashedAt: t, LayoutID: "bqMJp", RevisionID: rev}
}

// Append then List round-trips in flash order; Latest and Find read off the
// same files.
func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	for _, r := range []Record{
		rec("2026-07-21T10:00:00Z", "aaa111"),
		rec("2026-07-21T11:00:00Z", "bbb222"),
		rec("2026-07-21T12:00:00Z", "aaa111"), // rollback re-deploy
	} {
		if _, err := Append(dir, r); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	recs, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("len = %d, want 3", len(recs))
	}
	if recs[0].RevisionID != "aaa111" || recs[1].RevisionID != "bbb222" {
		t.Errorf("order = %s,%s want aaa111,bbb222", recs[0].RevisionID, recs[1].RevisionID)
	}
	last, err := Latest(dir)
	if err != nil || last == nil || !last.FlashedAt.Equal(recs[2].FlashedAt) {
		t.Errorf("Latest = %+v, %v", last, err)
	}
	found, err := Find(dir, "bbb222")
	if err != nil || found == nil || found.RevisionID != "bbb222" {
		t.Errorf("Find(bbb222) = %+v, %v", found, err)
	}
	missing, err := Find(dir, "zzz999")
	if err != nil || missing != nil {
		t.Errorf("Find(zzz999) = %+v, %v, want nil,nil", missing, err)
	}
}

// A store that has never been written is an empty history, not an error.
func TestEmptyStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")
	recs, err := List(dir)
	if err != nil || recs != nil {
		t.Errorf("List = %v, %v, want nil,nil", recs, err)
	}
	last, err := Latest(dir)
	if err != nil || last != nil {
		t.Errorf("Latest = %v, %v, want nil,nil", last, err)
	}
}

// Revision hashes land in filenames; anything but Oryx's alnum slugs is
// rejected before touching the filesystem.
func TestHashValidation(t *testing.T) {
	dir := t.TempDir()
	if _, err := Append(dir, rec("2026-07-21T10:00:00Z", "../evil")); err == nil {
		t.Error("Append accepted a path-traversal revision hash")
	}
	if _, err := FirmwarePath(dir, "a/b"); err == nil {
		t.Error("FirmwarePath accepted a slashed revision hash")
	}
	p, err := FirmwarePath(dir, "9DYwNW")
	if err != nil || p != filepath.Join(dir, "firmware", "9DYwNW.bin") {
		t.Errorf("FirmwarePath = %q, %v", p, err)
	}
}
