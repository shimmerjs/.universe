package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// touchReport builds a synthetic report 0x0D with the given slots populated.
func touchReport(scan uint16, count uint8, slots map[int]Contact) []byte {
	b := make([]byte, touchReportLen)
	b[0] = reportTouch
	for i, c := range slots {
		off := 1 + i*5
		b[off] = c.ID << 4
		if c.Tip {
			b[off] |= 0x01
		}
		binary.LittleEndian.PutUint16(b[off+1:off+3], c.X)
		binary.LittleEndian.PutUint16(b[off+3:off+5], c.Y)
	}
	binary.LittleEndian.PutUint16(b[51:53], scan)
	b[53] = count
	return b
}

func TestParseTouchReport(t *testing.T) {
	two := touchReport(1234, 2, map[int]Contact{
		0: {ID: 0, Tip: true, X: 100, Y: 200},
		1: {ID: 1, Tip: true, X: 16383, Y: 9599},
	})
	f, ok := parseTouchReport(42, two)
	if !ok {
		t.Fatal("two-contact frame did not parse")
	}
	if f.T != 42 || f.Scan != 1234 || f.Count != 2 {
		t.Fatalf("header: %+v", f)
	}
	if len(f.Contacts) != 2 {
		t.Fatalf("contacts: %+v", f.Contacts)
	}
	if f.Contacts[1] != (Contact{ID: 1, Tip: true, X: 16383, Y: 9599}) {
		t.Fatalf("contact 1: %+v", f.Contacts[1])
	}

	// lift: count covers the slot, tip cleared, stale coords intact
	lift := touchReport(1300, 1, map[int]Contact{0: {ID: 0, Tip: false, X: 100, Y: 200}})
	f, ok = parseTouchReport(43, lift)
	if !ok || len(f.Contacts) != 1 || f.Contacts[0].Tip {
		t.Fatalf("lift frame: ok=%v %+v", ok, f)
	}

	// tip set past count still included (hybrid-style continuation)
	hybrid := touchReport(1400, 0, map[int]Contact{3: {ID: 3, Tip: true, X: 5, Y: 6}})
	f, ok = parseTouchReport(44, hybrid)
	if !ok || len(f.Contacts) != 1 || f.Contacts[0].ID != 3 {
		t.Fatalf("hybrid frame: ok=%v %+v", ok, f)
	}

	// empty frame marshals contacts as [], not null
	empty := touchReport(1500, 0, nil)
	f, _ = parseTouchReport(45, empty)
	line, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"t":45,"scan":1500,"count":0,"contacts":[]}`
	if string(line) != want {
		t.Fatalf("ndjson shape: got %s want %s", line, want)
	}

	if _, ok := parseTouchReport(0, []byte{0x01, 0x02}); ok {
		t.Fatal("short report parsed")
	}
	other := touchReport(0, 0, nil)
	other[0] = 0x01
	if _, ok := parseTouchReport(0, other); ok {
		t.Fatal("non-touch report parsed")
	}
}

// -config without -daemon would silently ignore the file (modules are only
// resolved on the daemon path); it errors like the other combo guards.
func TestRunConfigRequiresDaemon(t *testing.T) {
	if err := run(context.Background(), options{config: "f"}); err == nil {
		t.Fatal("-config without -daemon did not error")
	}
}

func TestRecordingRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cap.txt")
	rec, err := newRecorder(path)
	if err != nil {
		t.Fatal(err)
	}
	rec.write(1000, []byte{0x0D, 0xAA, 0xBB})
	rec.write(2000, []byte{0x01, 0x02})
	if err := rec.close(); err != nil {
		t.Fatal(err)
	}

	reports, err := loadRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 || reports[0].t != 1000 || reports[1].t != 2000 {
		t.Fatalf("reports: %+v", reports)
	}
	if reports[0].raw[0] != 0x0D || len(reports[0].raw) != 3 {
		t.Fatalf("raw: %x", reports[0].raw)
	}
}

func TestOfferDropsOldest(t *testing.T) {
	ch := make(chan []byte, 2)
	offer(ch, []byte("a"))
	offer(ch, []byte("b"))
	offer(ch, []byte("c"))
	if len(ch) != 2 {
		t.Fatalf("queue len %d", len(ch))
	}
	if got := string(<-ch); got != "b" {
		t.Fatalf("oldest survivor %q, want b", got)
	}
	if got := string(<-ch); got != "c" {
		t.Fatalf("newest %q, want c", got)
	}
}

// The reopen schedule is the runningboardd-load contract: a chronic open
// failure must converge to reconnectCap, and only success or a failure-class
// flip may put it back on the fast ramp.
func TestReopenBackoff(t *testing.T) {
	var b reopenBackoff

	// chronic same-class failure doubles from reconnectMin and pins at cap
	want := reconnectMin
	for i := range 12 {
		if got := b.fail(false); got != want {
			t.Fatalf("attempt %d: wait %s, want %s", i, got, want)
		}
		want = min(want*2, reconnectCap)
	}
	if got := b.fail(false); got != reconnectCap {
		t.Fatalf("steady state %s, want cap %s", got, reconnectCap)
	}

	// class flip (unplug/replug = device-set change) resets to the floor
	if got := b.fail(true); got != reconnectMin {
		t.Fatalf("flip to absent: %s, want %s", got, reconnectMin)
	}
	if got := b.fail(true); got != 2*reconnectMin {
		t.Fatalf("absent again: %s, want %s", got, 2*reconnectMin)
	}
	// chronic absence pins at absentCap, not reconnectCap: the timer is the
	// only replug observer, so its ceiling is the worst-case reattach latency
	for range 10 {
		b.fail(true)
	}
	if got := b.fail(true); got != absentCap {
		t.Fatalf("chronic absent: %s, want absent cap %s", got, absentCap)
	}
	if got := b.fail(false); got != reconnectMin {
		t.Fatalf("flip to present: %s, want %s", got, reconnectMin)
	}

	// successful open resets even when the next failure is the same class
	b.fail(false)
	b.reset()
	if got := b.fail(false); got != reconnectMin {
		t.Fatalf("after reset: %s, want %s", got, reconnectMin)
	}
}

// The absent class must survive the not-found wrap (the reopen loops key on
// it), and an open failure on a present device must not classify as absent.
func TestOpenErrorClass(t *testing.T) {
	err := noCollectionErr(edgeVID, edgePID, usagePageDigitizer, usageTouchScreen)
	if !errors.Is(err, errAbsent) {
		t.Fatalf("not-found error not classified absent: %v", err)
	}
	want := "no 27C0:0859 collection with usage_page=0x0D usage=0x04 (device connected?)"
	if err.Error() != want {
		t.Fatalf("message %q, want %q", err.Error(), want)
	}
	seized := fmt.Errorf("open (Input Monitoring granted?): %w",
		errors.New("hid_open_path: failed to open IOHID device: (0xE00002C5) exclusive access and device already open"))
	if errors.Is(seized, errAbsent) {
		t.Fatal("present-but-seized error misclassified as absent")
	}
}

// The broadcaster's sockets are owner-only: keys.sock is a keystroke feed,
// so umask perms would leave it open to any local user.
func TestBroadcasterSocketMode(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "t.sock")
	b, err := newBroadcaster(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %o, want 0600", fi.Mode().Perm())
	}
}

func waitForClient(t *testing.T, b *broadcaster) {
	t.Helper()
	for i := 0; b.clientCount() == 0; i++ {
		if i > 100 {
			t.Fatal("client never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBroadcastNDJSON(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "t.sock")
	b, err := newBroadcaster(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitForClient(t, b)

	f, _ := parseTouchReport(99, touchReport(7, 1, map[int]Contact{0: {ID: 2, Tip: true, X: 10, Y: 20}}))
	b.publishJSON(f)

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var got Frame
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("bad ndjson %q: %v", line, err)
	}
	if got.T != 99 || got.Count != 1 || len(got.Contacts) != 1 || got.Contacts[0].X != 10 {
		t.Fatalf("frame over socket: %+v", got)
	}
}

// TestReplayEndToEnd drives the full fingerless path: recording file ->
// loadRecording -> runReplay -> broadcaster -> socket client.
func TestReplayEndToEnd(t *testing.T) {
	dir := t.TempDir()
	capPath := filepath.Join(dir, "cap.txt")
	rep1 := touchReport(2002, 1, map[int]Contact{0: {ID: 1, Tip: true, X: 100, Y: 200}})
	rep2 := touchReport(2010, 1, map[int]Contact{0: {ID: 1, Tip: false, X: 100, Y: 200}})
	lines := fmt.Sprintf("%d %s\n%d %s\n",
		1_000_000_000, hex.EncodeToString(rep1),
		1_050_000_000, hex.EncodeToString(rep2))
	if err := os.WriteFile(capPath, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}

	reports, err := loadRecording(capPath)
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "r.sock")
	b, err := newBroadcaster(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()

	done := make(chan error, 1)
	go func() { done <- runReplay(context.Background(), b, reports) }()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)

	var frames []Frame
	for range 2 {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v (got %d frames)", err, len(frames))
		}
		var f Frame
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("bad ndjson %q: %v", line, err)
		}
		frames = append(frames, f)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !frames[0].Contacts[0].Tip || frames[1].Contacts[0].Tip {
		t.Fatalf("tip sequence: %+v", frames)
	}
	if d := frames[1].T - frames[0].T; d != 50_000_000 {
		t.Fatalf("rebased delta %dns, want 50ms", d)
	}
}
