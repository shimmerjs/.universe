// Package usbserial reads the deployed layout identity off the Moonlander's
// USB serial number. Oryx-built firmware advertises "<layoutId>/<revisionId>"
// there (e.g. "bqMJp/9DYwNW"), re-enumerated after every flash, so the serial
// is ground truth for what is RUNNING on the board -- not what exists in
// Oryx. The reader execs the host ioreg (macOS ships /usr/sbin/ioreg)
// instead of linking an HID/IOKit dependency -- exec-a-host-binary is the
// house idiom.
package usbserial

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// zsaVendorID is ZSA Technology Labs' USB vendor id (0x3297). Matching on
// vendor alone keeps the reader board-agnostic; the geometry pin lives in
// the oryx package.
const zsaVendorID = 12951

// Identity is the deployed layout identity: the two Oryx hashes the
// firmware packs into the serial.
type Identity struct {
	LayoutID   string
	RevisionID string
}

// ErrNotPresent means no ZSA device is on the USB bus: unplugged, or
// re-enumerated into the DFU bootloader mid-flash (the bootloader does not
// carry the Oryx serial).
var ErrNotPresent = errors.New("usbserial: no ZSA keyboard on the bus")

// execTimeout bounds one ioreg exec; the registry dump is small, so a slow
// call means a wedged binary, not a big read.
const execTimeout = 10 * time.Second

// ioregBin resolves the ioreg CLI: the macOS system binary first, PATH as
// the fallback. Tests gate on this same resolution (skip-on-missing).
func ioregBin() (string, error) {
	const sys = "/usr/sbin/ioreg"
	if _, err := exec.LookPath(sys); err == nil {
		return sys, nil
	}
	return exec.LookPath("ioreg")
}

// Read execs ioreg and returns the first ZSA device's identity.
func Read(ctx context.Context) (Identity, error) {
	bin, err := ioregBin()
	if err != nil {
		return Identity{}, fmt.Errorf("usbserial: ioreg unavailable: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-p", "IOUSB", "-l", "-w0").Output()
	if err != nil {
		// surface the child's stderr (house Output() idiom): "exit status 1"
		// alone diagnoses nothing
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return Identity{}, fmt.Errorf("usbserial: ioreg: %w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return Identity{}, fmt.Errorf("usbserial: ioreg: %w", err)
	}
	return parse(strings.NewReader(string(out)))
}

// parse scans an ioreg -p IOUSB -l dump. Devices are "+-o Name@loc" headers
// followed by a brace block of `"key" = value` lines; the scan folds each
// block into a flat map and takes the first block whose idVendor is ZSA's.
func parse(r io.Reader) (Identity, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	props := map[string]string{}
	var found map[string]string
	flush := func() {
		if found == nil && props["idVendor"] == strconv.Itoa(zsaVendorID) {
			found = props
		}
		props = map[string]string{}
	}
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "+-o ") {
			flush()
			continue
		}
		k, v, ok := propLine(line)
		if !ok {
			continue
		}
		props[k] = v
	}
	flush()
	if err := sc.Err(); err != nil {
		return Identity{}, fmt.Errorf("usbserial: scan ioreg: %w", err)
	}
	if found == nil {
		return Identity{}, ErrNotPresent
	}
	var id Identity
	serial := found["USB Serial Number"]
	var ok bool
	if id.LayoutID, id.RevisionID, ok = splitSerial(serial); !ok {
		return Identity{}, fmt.Errorf("usbserial: serial %q is not <layout>/<revision> -- not oryx firmware?", serial)
	}
	return id, nil
}

// propLine extracts one `"key" = value` registry line, stripping the tree
// decoration and unquoting string values. Non-property lines report !ok.
func propLine(line string) (key, val string, ok bool) {
	s := strings.TrimLeft(line, " |")
	if !strings.HasPrefix(s, `"`) {
		return "", "", false
	}
	k, v, ok := strings.Cut(s[1:], `" = `)
	if !ok {
		return "", "", false
	}
	return k, strings.Trim(v, `"`), true
}

// splitSerial validates the Oryx serial shape: two non-empty alphanumeric
// hashes joined by a slash (the slugs Oryx issues are alnum-only).
func splitSerial(s string) (layout, revision string, ok bool) {
	layout, revision, ok = strings.Cut(s, "/")
	if !ok || layout == "" || revision == "" {
		return "", "", false
	}
	for _, r := range layout + revision {
		alnum := r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !alnum {
			return "", "", false
		}
	}
	return layout, revision, true
}

// Poller memoizes Read behind a TTL so per-frame callers (dock ticks,
// kuiboard renders) stay bounded: at most one ioreg exec per TTL window,
// errors cached the same as hits so an unplugged board does not re-exec
// every frame. Get holds the lock across a refresh, so concurrent callers
// serialize behind one exec (both hosts drive it from a single goroutine;
// sharing a Poller across goroutines accepts up to execTimeout of stall).
type Poller struct {
	TTL time.Duration
	// Read is the underlying serial read; nil means the package Read
	// (a seam so hosts test without a bus).
	Read func(context.Context) (Identity, error)

	mu  sync.Mutex
	at  time.Time
	id  Identity
	err error
}

// Get returns the cached identity, refreshing when the TTL lapsed.
func (p *Poller) Get(ctx context.Context) (Identity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.at.IsZero() && time.Since(p.at) < p.TTL {
		return p.id, p.err
	}
	read := p.Read
	if read == nil {
		read = Read
	}
	p.id, p.err = read(ctx)
	p.at = time.Now()
	return p.id, p.err
}

// Invalidate drops the cache so the next Get re-reads immediately (flash
// verification polls the serial faster than the display TTL).
func (p *Poller) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.at = time.Time{}
}
