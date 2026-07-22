// Package flash drives one firmware deployment end to end: resolve what the
// board is running (USB serial), fetch the target revision from Oryx,
// download and md5-verify the firmware, exec zapp (which waits for the
// user's RESET tap -- stock firmware has no programmatic bootloader entry),
// then poll the re-enumerated serial until the board itself reports the
// target revision. Only that serial readback counts as deployed; the
// generation record is written after it.
package flash

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/generations"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/oryx"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/usbserial"
)

// Step names one phase of the pipeline for progress display.
type Step int

const (
	// StepResolve is reading the board's identity off the USB serial.
	StepResolve Step = iota
	// StepMeta is fetching the target revision's layout meta from Oryx.
	StepMeta
	// StepDownload is downloading (or adopting the archived) firmware.
	StepDownload
	// StepZapp is zapp running: the board needs its RESET key tapped.
	StepZapp
	// StepVerify is polling the serial for the re-enumerated board.
	StepVerify
	// StepRecord is writing the generation record.
	StepRecord
)

// Event is one progress emission: the active step and a human line.
type Event struct {
	Step Step
	Info string
}

// ErrAlreadyDeployed means the board already runs the target revision;
// callers treat it as a benign outcome, not a failure.
var ErrAlreadyDeployed = errors.New("flash: board already runs the target revision")

// verifyWindow bounds the post-zapp wait for the board to re-enumerate with
// the new serial. Generous: the STM32 reboot plus USB settle is seconds.
const verifyWindow = 60 * time.Second

// verifyPoll is the serial re-read cadence inside the window.
const verifyPoll = time.Second

// metaTimeout and downloadTimeout bound the two network steps (the default
// http client has no timeout of its own, and a hung fetch would pin the
// host's flash UI forever); zapp's RESET wait stays unbounded by design --
// the user taps when ready.
const (
	metaTimeout     = 30 * time.Second
	downloadTimeout = 3 * time.Minute
)

// maxFirmwareBytes caps a firmware download; a body past it is rejected,
// never silently truncated into an md5 mismatch (or worse, an unverified
// archive). Real Moonlander images run well under 1 MiB.
const maxFirmwareBytes = 8 << 20

// Runner is one configured deployment. Zero-value fields resolve to the
// real environment; the func fields are seams so tests drive the pipeline
// with no board, network, or zapp.
type Runner struct {
	// GenDir is the generations store; "" resolves DefaultDir.
	GenDir string
	// Zapp is the flasher binary; "" resolves PATH then the per-user
	// home-manager profile bin (the module.nix convention).
	Zapp string
	// Target is the revision hash to deploy; "" deploys latest.
	Target string
	// Emit receives progress events; nil discards them.
	Emit func(Event)
	// VerifyWindow/VerifyPoll bound the post-zapp serial readback; zero
	// takes the package defaults (seams so tests run in milliseconds and
	// can exercise the window-closed paths).
	VerifyWindow time.Duration
	VerifyPoll   time.Duration

	ReadIdentity func(context.Context) (usbserial.Identity, error)
	FetchLayout  func(ctx context.Context, hashID, revisionID string) (*oryx.Layout, error)
	FirmwareURL  func(hashID, revisionID string) string
	RunZapp      func(ctx context.Context, bin, firmware string, info func(string)) error
	Now          func() time.Time
}

func (r *Runner) emit(step Step, info string) {
	if r.Emit != nil {
		r.Emit(Event{Step: step, Info: info})
	}
}

// Run executes the pipeline and returns the written generation record.
func (r *Runner) Run(ctx context.Context) (*generations.Record, error) {
	readIdentity := r.ReadIdentity
	if readIdentity == nil {
		readIdentity = usbserial.Read
	}
	fetchLayout := r.FetchLayout
	if fetchLayout == nil {
		// the META fetch, not the caching one: the layout disk cache is the
		// current-board snapshot, and the target stays undeployed until the
		// serial readback proves otherwise -- an aborted flash must not
		// leave the cache naming firmware the board never adopted. The
		// verified payload persists via the generation record instead.
		fetchLayout = oryx.FetchLayoutMeta
	}
	fwURL := r.FirmwareURL
	if fwURL == nil {
		fwURL = oryx.FirmwareURL
	}
	runZapp := r.RunZapp
	if runZapp == nil {
		runZapp = execZapp
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	genDir := r.GenDir
	if genDir == "" {
		d, err := generations.DefaultDir()
		if err != nil {
			return nil, err
		}
		genDir = d
	}

	r.emit(StepResolve, "reading board identity")
	id, err := readIdentity(ctx)
	if err != nil {
		return nil, fmt.Errorf("flash: resolve board: %w", err)
	}

	target := r.Target
	if target == "" {
		target = oryx.RevisionLatest
	}
	r.emit(StepMeta, "fetching revision "+target)
	mctx, cancelMeta := context.WithTimeout(ctx, metaTimeout)
	meta, err := fetchLayout(mctx, id.LayoutID, target)
	cancelMeta()
	if err != nil {
		return nil, fmt.Errorf("flash: fetch revision meta: %w", err)
	}
	if meta.RevisionID == id.RevisionID {
		return nil, fmt.Errorf("%w (%s)", ErrAlreadyDeployed, id.RevisionID)
	}

	fw, err := generations.FirmwarePath(genDir, meta.RevisionID)
	if err != nil {
		return nil, err
	}
	if md5Matches(fw, meta.MD5) {
		r.emit(StepDownload, "using archived firmware "+filepath.Base(fw))
	} else {
		r.emit(StepDownload, "downloading firmware "+meta.RevisionID)
		dctx, cancelDL := context.WithTimeout(ctx, downloadTimeout)
		err := download(dctx, fwURL(id.LayoutID, meta.RevisionID), fw, meta.MD5)
		cancelDL()
		if err != nil {
			return nil, err
		}
	}

	bin := r.Zapp
	if bin == "" {
		if bin, err = zappBin(); err != nil {
			return nil, err
		}
	}
	r.emit(StepZapp, "tap RESET on the board")
	if err := runZapp(ctx, bin, fw, func(line string) { r.emit(StepZapp, line) }); err != nil {
		return nil, fmt.Errorf("flash: zapp: %w", err)
	}

	r.emit(StepVerify, "waiting for the board to report "+meta.RevisionID)
	if err := r.waitForRevision(ctx, readIdentity, meta.RevisionID); err != nil {
		return nil, err
	}

	r.emit(StepRecord, "recording generation "+meta.RevisionID)
	rec := generations.Record{
		FlashedAt:      now(),
		LayoutID:       id.LayoutID,
		RevisionID:     meta.RevisionID,
		PrevRevisionID: id.RevisionID,
		QmkVersion:     meta.QmkVersion,
		MD5:            meta.MD5,
		Title:          meta.Title,
		Layout:         meta,
	}
	if _, err := generations.Append(genDir, rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// waitForRevision polls the serial until the re-enumerated board reports
// target. ANY error mid-window is retryable -- ErrNotPresent is the normal
// DFU phase, and an ioreg hiccup or a mid-enumeration parse error must not
// abort a verify whose flash already ran; a window that closes without the
// target reports the last thing seen (old revision, or the sticking error).
func (r *Runner) waitForRevision(ctx context.Context, read func(context.Context) (usbserial.Identity, error), target string) error {
	window, poll := r.VerifyWindow, r.VerifyPoll
	if window == 0 {
		window = verifyWindow
	}
	if poll == 0 {
		poll = verifyPoll
	}
	deadline := time.Now().Add(window)
	last := ""
	var lastErr error
	for {
		id, err := read(ctx)
		switch {
		case err == nil && id.RevisionID == target:
			return nil
		case err == nil:
			last = id.RevisionID
		case !errors.Is(err, usbserial.ErrNotPresent):
			lastErr = err
		}
		if time.Now().After(deadline) {
			switch {
			case last != "":
				return fmt.Errorf("flash: verify: board reports %s, want %s -- zapp exited clean but the serial never adopted the target", last, target)
			case lastErr != nil:
				return fmt.Errorf("flash: verify: %w", lastErr)
			}
			return errors.New("flash: verify: board never re-enumerated -- still in the bootloader?")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// download fetches url to dst via a .part rename, enforcing wantMD5 when
// Oryx supplied one (revision.md5 is the firmware file's md5).
func download(ctx context.Context, url, dst, wantMD5 string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("flash: download: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("flash: download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("flash: download: %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFirmwareBytes+1))
	if err != nil {
		return fmt.Errorf("flash: download: %w", err)
	}
	if len(raw) == 0 {
		return errors.New("flash: download: empty firmware body")
	}
	if len(raw) > maxFirmwareBytes {
		return errors.New("flash: download: firmware body exceeds the size cap")
	}
	if wantMD5 != "" {
		sum := md5.Sum(raw)
		if got := hex.EncodeToString(sum[:]); got != wantMD5 {
			return fmt.Errorf("flash: download: md5 %s, want %s", got, wantMD5)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("flash: download: %w", err)
	}
	part := dst + ".part"
	if err := os.WriteFile(part, raw, 0o600); err != nil {
		return fmt.Errorf("flash: download: %w", err)
	}
	if err := os.Rename(part, dst); err != nil {
		return fmt.Errorf("flash: download: %w", err)
	}
	return nil
}

// md5Matches reports whether path exists and hashes to want; want=="" never
// matches (no hash means no basis to trust an archive).
func md5Matches(path, want string) bool {
	if want == "" {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	sum := md5.Sum(raw)
	return hex.EncodeToString(sum[:]) == want
}

// zappBin resolves the flasher: PATH first, then the per-user home-manager
// profile bin (kuiboard may run under a launchd PATH that lacks it).
func zappBin() (string, error) {
	if p, err := exec.LookPath("zapp"); err == nil {
		return p, nil
	}
	if u, err := user.Current(); err == nil {
		p := filepath.Join("/etc/profiles/per-user", u.Username, "bin", "zapp")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("flash: zapp not on PATH (is the flake package deployed?)")
}

// execZapp runs `zapp flash <firmware>` streaming its output lines (both
// pipes; indicatif redraws with carriage returns, so CR splits like LF) to
// info. zapp itself blocks until the RESET tap re-enumerates the bootloader.
func execZapp(ctx context.Context, bin, firmware string, info func(string)) error {
	cmd := exec.CommandContext(ctx, bin, "flash", firmware)
	w := &lineWriter{info: info}
	cmd.Stdout = w
	cmd.Stderr = w
	err := cmd.Run()
	w.flush()
	return err
}

// lineWriter splits a byte stream into display lines on LF or CR and hands
// the non-empty ones to info.
type lineWriter struct {
	info func(string)
	buf  bytes.Buffer
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		raw := w.buf.Bytes()
		i := bytes.IndexAny(raw, "\r\n")
		if i < 0 {
			return len(p), nil
		}
		w.send(string(raw[:i]))
		w.buf.Next(i + 1)
	}
}

func (w *lineWriter) flush() { w.send(w.buf.String()); w.buf.Reset() }

func (w *lineWriter) send(s string) {
	// indicatif redraws carry erase/cursor escapes beside the CRs; hosts
	// render these lines into their own TUI rows, so strip, never forward
	s = strings.TrimSpace(ansi.Strip(s))
	if s != "" && w.info != nil {
		w.info(s)
	}
}
