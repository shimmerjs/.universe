package flash

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/keyboard/generations"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/oryx"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/usbserial"
)

var fwBytes = []byte("not-a-real-stm32-image")

func fwMD5() string {
	sum := md5.Sum(fwBytes)
	return hex.EncodeToString(sum[:])
}

// identitySeq returns a ReadIdentity seam that plays a scripted sequence:
// the resolve read, then the verify polls (DFU absence, then the new rev).
func identitySeq(t *testing.T, seq []func() (usbserial.Identity, error)) func(context.Context) (usbserial.Identity, error) {
	t.Helper()
	i := 0
	return func(context.Context) (usbserial.Identity, error) {
		if i >= len(seq) {
			t.Fatal("ReadIdentity called past the scripted sequence")
		}
		f := seq[i]
		i++
		return f()
	}
}

func present(rev string) func() (usbserial.Identity, error) {
	return func() (usbserial.Identity, error) {
		return usbserial.Identity{LayoutID: "bqMJp", RevisionID: rev}, nil
	}
}

func absent() (usbserial.Identity, error) {
	return usbserial.Identity{}, usbserial.ErrNotPresent
}

func testRunner(t *testing.T, srvURL string) *Runner {
	t.Helper()
	return &Runner{
		GenDir:     t.TempDir(),
		Zapp:       "/dev/null", // never exec'd; RunZapp seam covers it
		VerifyPoll: time.Millisecond,
		FetchLayout: func(_ context.Context, hashID, revisionID string) (*oryx.Layout, error) {
			if hashID != "bqMJp" {
				t.Errorf("fetch hashID = %q", hashID)
			}
			return &oryx.Layout{HashID: hashID, RevisionID: "newRev", MD5: fwMD5(), Title: "aw4", QmkVersion: "25.0"}, nil
		},
		FirmwareURL: func(hashID, revisionID string) string {
			return srvURL + "/" + hashID + "/" + revisionID + "/binary"
		},
		RunZapp: func(_ context.Context, bin, firmware string, info func(string)) error {
			info("flashing " + filepath.Base(firmware))
			return nil
		},
		Now: func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) },
	}
}

func fwServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(fwBytes)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The full pipeline: old revision resolved, firmware downloaded and
// verified, zapp run, serial readback adopts the target (with a DFU absence
// mid-window), generation recorded with the payload and prev pointer.
func TestRunHappyPath(t *testing.T) {
	srv := fwServer(t)
	r := testRunner(t, srv.URL)
	r.ReadIdentity = identitySeq(t, []func() (usbserial.Identity, error){
		present("oldRev"), // resolve
		absent,            // verify: DFU phase
		present("newRev"), // verify: re-enumerated
	})
	var steps []Step
	r.Emit = func(e Event) { steps = append(steps, e.Step) }

	rec, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.RevisionID != "newRev" || rec.PrevRevisionID != "oldRev" || rec.Layout == nil {
		t.Errorf("record = %+v", rec)
	}
	fw, _ := generations.FirmwarePath(r.GenDir, "newRev")
	if !md5Matches(fw, fwMD5()) {
		t.Error("firmware not archived with the expected bytes")
	}
	recs, err := generations.List(r.GenDir)
	if err != nil || len(recs) != 1 {
		t.Fatalf("List = %d records, %v", len(recs), err)
	}
	sawZapp := false
	for _, s := range steps {
		if s == StepZapp {
			sawZapp = true
		}
	}
	if !sawZapp {
		t.Error("no StepZapp event emitted (the RESET prompt rides on it)")
	}
}

// A board already on the target revision is the benign no-op outcome.
func TestRunAlreadyDeployed(t *testing.T) {
	r := testRunner(t, "http://unused.invalid")
	r.ReadIdentity = identitySeq(t, []func() (usbserial.Identity, error){present("newRev")})
	_, err := r.Run(context.Background())
	if !errors.Is(err, ErrAlreadyDeployed) {
		t.Fatalf("err = %v, want ErrAlreadyDeployed", err)
	}
}

// A corrupted download must fail before zapp ever runs.
func TestRunMD5Mismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("wrong bytes"))
	}))
	defer srv.Close()
	r := testRunner(t, srv.URL)
	r.ReadIdentity = identitySeq(t, []func() (usbserial.Identity, error){present("oldRev")})
	r.RunZapp = func(context.Context, string, string, func(string)) error {
		t.Fatal("zapp ran on a bad download")
		return nil
	}
	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("Run accepted an md5 mismatch")
	}
}

// An archived bin with the right md5 skips the network entirely: rollback
// keeps working offline or after Oryx drops the revision.
func TestRunUsesArchive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()
	r := testRunner(t, srv.URL)
	fw, err := generations.FirmwarePath(r.GenDir, "newRev")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(fw), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fw, fwBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	r.ReadIdentity = identitySeq(t, []func() (usbserial.Identity, error){
		present("oldRev"),
		present("newRev"),
	})
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// A transient serial error mid-window (an ioreg hiccup, a mid-enumeration
// parse failure) is retried, never fatal: zapp already ran, so aborting
// the verify would lose a successful flash's record.
func TestRunVerifyToleratesTransientError(t *testing.T) {
	srv := fwServer(t)
	r := testRunner(t, srv.URL)
	r.ReadIdentity = identitySeq(t, []func() (usbserial.Identity, error){
		present("oldRev"), // resolve
		func() (usbserial.Identity, error) {
			return usbserial.Identity{}, errors.New("usbserial: ioreg: exit status 1")
		},
		present("newRev"),
	})
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// A verify window that closes on the OLD revision reports the mismatch;
// one that closes on a sticking error reports that error.
func TestRunVerifyWindowCloses(t *testing.T) {
	srv := fwServer(t)
	r := testRunner(t, srv.URL)
	r.VerifyWindow = 20 * time.Millisecond
	r.ReadIdentity = func(context.Context) (usbserial.Identity, error) { return present("oldRev")() }
	_, err := r.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "board reports oldRev") {
		t.Fatalf("err = %v, want the old-revision mismatch", err)
	}

	r = testRunner(t, srv.URL)
	r.VerifyWindow = 20 * time.Millisecond
	stuck := errors.New("usbserial: ioreg: exit status 1")
	first := true
	r.ReadIdentity = func(context.Context) (usbserial.Identity, error) {
		if first {
			first = false
			return present("oldRev")()
		}
		return usbserial.Identity{}, stuck
	}
	if _, err := r.Run(context.Background()); !errors.Is(err, stuck) {
		t.Fatalf("err = %v, want the sticking read error", err)
	}
}

// lineWriter splits on both LF and CR (indicatif redraws with CR), strips
// the ANSI escapes indicatif interleaves, and drops blank segments.
func TestLineWriter(t *testing.T) {
	var got []string
	w := &lineWriter{info: func(s string) { got = append(got, s) }}
	w.Write([]byte("downloading\r\x1b[2Kprogress 50%\rprogress \x1b[1m100%\x1b[0m\ndone"))
	w.flush()
	want := []string{"downloading", "progress 50%", "progress 100%", "done"}
	if len(got) != len(want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}
