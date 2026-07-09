// Package ax is the direct macOS Accessibility (AX) client behind the
// dock-mirror module's per-window unminimize: it walks the Dock's own AX
// tree for AXMinimizedWindowDockItem entries and presses them by EXACT
// title, with an in-app AXMinimized-write fallback for the tap-vs-sweep
// race. One TCC grant total: Accessibility on the fixed-path khudson
// binary.
package ax

/*
#cgo darwin LDFLAGS: -framework ApplicationServices
#include <stdlib.h>
#include <ApplicationServices/ApplicationServices.h>

// axEnsureTrusted wraps AXIsProcessTrustedWithOptions so the option
// dictionary (kAXTrustedCheckOptionPrompt) stays in C. prompt fires the
// System Settings dialog even from a launchd agent (yabai precedent);
// the process must restart after granting.
static int axEnsureTrusted(int prompt) {
	const void *keys[] = { kAXTrustedCheckOptionPrompt };
	const void *vals[] = { prompt ? kCFBooleanTrue : kCFBooleanFalse };
	CFDictionaryRef opts = CFDictionaryCreate(kCFAllocatorDefault, keys, vals, 1,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	int t = AXIsProcessTrustedWithOptions(opts) ? 1 : 0;
	CFRelease(opts);
	return t;
}
*/
import "C"

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// Typed errors, distinguishable via errors.Is/As; press and attribute-set
// failures wrap *AXError instead.
var (
	// ErrUntrusted means the process lacks the Accessibility grant.
	ErrUntrusted = errors.New("not trusted for accessibility")
	// ErrNotFound means no dock item / window matched the exact title.
	ErrNotFound = errors.New("not found")
)

// AXError is a nonzero ApplicationServices AXError from a press or
// attribute call (code 0 marks a call that reported success but did not
// take effect, e.g. an AXMinimized write whose readback still says true).
type AXError struct {
	Op   string
	Code int
}

func (e *AXError) Error() string { return fmt.Sprintf("ax: %s: AXError %d", e.Op, e.Code) }

// axErrAPIDisabled is kAXErrorAPIDisabled (AXError.h): the AX API refused
// the call because the process lacks the Accessibility grant.
const axErrAPIDisabled = -25211

// mapAXError types a nonzero AXError code: the API-disabled code is the
// untrusted state (so callers see ErrUntrusted however it surfaces),
// everything else wraps as *AXError.
func mapAXError(op string, code int) error {
	if code == axErrAPIDisabled {
		return fmt.Errorf("ax: %s: %w", op, ErrUntrusted)
	}
	return &AXError{Op: op, Code: code}
}

// notFound wraps ErrNotFound with what was missing.
func notFound(what string) error { return fmt.Errorf("ax: %s: %w", what, ErrNotFound) }

// titleMatches is the one matching rule for dock items and windows:
// EXACT equality. Pressing or unminimizing a near-miss (case, prefix,
// whitespace) would act on somebody else's window.
func titleMatches(got, want string) bool { return got == want }

// Attribute and action names, built once (CFStringCreateWithCString);
// they live for the process, never released.
var (
	attrChildren  = cfStr("AXChildren")
	attrRole      = cfStr("AXRole")
	attrSubrole   = cfStr("AXSubrole")
	attrTitle     = cfStr("AXTitle")
	attrWindows   = cfStr("AXWindows")
	attrMinimized = cfStr("AXMinimized")
	actionPress   = cfStr("AXPress")
	actionRaise   = cfStr("AXRaise")
)

func cfStr(s string) C.CFStringRef {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	return C.CFStringCreateWithCString(C.kCFAllocatorDefault, cs, C.kCFStringEncodingUTF8)
}

// Trusted reports whether the process holds the Accessibility grant.
func Trusted() bool { return C.AXIsProcessTrusted() != 0 }

// axMsgTimeout caps every AX call from this process (systemwide element =
// process-global): the AX default lets each attribute call against a
// beachballed app block ~6s, unbounded across a sweep, and the poll
// budget is 5s.
const axMsgTimeout = 2.0

var msgTimeoutOnce sync.Once

// boundMessaging installs the process-global AX messaging timeout, once.
// Needs no trust; called before every walk so no path escapes the bound.
func boundMessaging() {
	msgTimeoutOnce.Do(func() {
		sw := C.AXUIElementCreateSystemWide()
		C.AXUIElementSetMessagingTimeout(sw, C.float(axMsgTimeout))
		release(C.CFTypeRef(sw))
	})
}

// EnsureTrusted checks the Accessibility grant; prompt=true fires the
// one-time System Settings dialog when it is missing. A fresh grant only
// takes effect after the process restarts.
func EnsureTrusted(prompt bool) bool {
	p := C.int(0)
	if prompt {
		p = 1
	}
	return C.axEnsureTrusted(p) != 0
}

// Item is one minimized-window Dock item. The name is the BARE window
// title -- the Dock exposes no owning-app attribute. Items are ephemeral
// snapshots: no AXUIElementRef is retained across calls, so pressing
// re-resolves by title.
type Item struct {
	Title string
}

// DockMinimizedItems walks the Dock's first AXList and returns the items
// with subrole AXMinimizedWindowDockItem. ctx bounds the WHOLE sweep (the
// poll budget); per-call blocking is separately capped by the global
// messaging timeout, so an abandoned walk drains in bounded time rather
// than leaking a goroutine forever.
func DockMinimizedItems(ctx context.Context) ([]Item, error) {
	type res struct {
		items []Item
		err   error
	}
	ch := make(chan res, 1)
	go func() {
		var items []Item
		err := walkDockMinimized(ctx, func(title string, _ C.AXUIElementRef) bool {
			items = append(items, Item{Title: title})
			return false
		})
		ch <- res{items, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.items, r.err
	}
}

// PressMinimizedItem re-walks the Dock and presses the item whose name is
// EXACTLY title. An absent item is ErrNotFound (the tap-vs-sweep race:
// the item can vanish between render and tap); a near-miss is NEVER
// pressed.
func PressMinimizedItem(title string) error {
	found := false
	var pressErr error
	err := walkDockMinimized(context.Background(), func(got string, item C.AXUIElementRef) bool {
		if !titleMatches(got, title) {
			return false
		}
		found = true
		if code := C.AXUIElementPerformAction(item, actionPress); code != C.kAXErrorSuccess {
			pressErr = mapAXError("press dock item", int(code))
		}
		return true
	})
	if err != nil {
		return err
	}
	if !found {
		return notFound(fmt.Sprintf("dock item %q", title))
	}
	return pressErr
}

// walkDockMinimized visits every AXMinimizedWindowDockItem in the Dock's
// first AXList. visit runs while the backing children array is still
// retained; refs must not escape the visit. Returning true stops the walk.
func walkDockMinimized(ctx context.Context, visit func(title string, item C.AXUIElementRef) bool) error {
	if !Trusted() {
		return ErrUntrusted
	}
	boundMessaging()
	pid, err := dockPid(ctx)
	if err != nil {
		return err
	}
	app := C.AXUIElementCreateApplication(C.pid_t(pid))
	defer release(C.CFTypeRef(app))

	kids, err := arrayAttr(app, attrChildren, "dock children")
	if err != nil {
		return err
	}
	defer release(C.CFTypeRef(kids))
	var list C.AXUIElementRef
	for i := 0; i < arrayLen(kids); i++ {
		el := arrayEl(kids, i)
		if role, err := stringAttr(el, attrRole, "dock child role"); err == nil && role == "AXList" {
			list = el
			break
		}
	}
	if list == 0 {
		return fmt.Errorf("ax: dock exposes no AXList child")
	}

	items, err := arrayAttr(list, attrChildren, "dock list children")
	if err != nil {
		return err
	}
	defer release(C.CFTypeRef(items))
	for i := 0; i < arrayLen(items); i++ {
		el := arrayEl(items, i)
		sub, err := stringAttr(el, attrSubrole, "dock item subrole")
		if err != nil || sub != "AXMinimizedWindowDockItem" {
			continue
		}
		title, err := stringAttr(el, attrTitle, "dock item title")
		if err != nil {
			continue
		}
		if visit(title, el) {
			return nil
		}
	}
	return nil
}

// Raise retry through the deminimize animation (DockDoor precedent: the
// first raise can land before the window is raisable).
const (
	raiseTries = 5
	raiseGap   = 100 * time.Millisecond
)

// UnminimizeWindow is the fallback for the tap-vs-sweep race only: when
// the Dock item vanished between render and tap, scan the app's own
// kAXWindows for AXTitle == title with kAXMinimized true, write
// kAXMinimized false, read it back (the write silently no-ops without
// confirmation), then raise + activate (`open -a`, the accepted lo-fi
// activate). kAXWindows omits windows on another Space -- those come
// back ErrNotFound.
func UnminimizeWindow(appName, title string) error {
	if !Trusted() {
		return ErrUntrusted
	}
	boundMessaging()
	out, err := exec.Command("lsappinfo", "list").Output()
	if err != nil {
		return fmt.Errorf("lsappinfo list: %w", err)
	}
	pids := parseAppPids(string(out), appName)
	if len(pids) == 0 {
		return notFound(fmt.Sprintf("app %q", appName))
	}
	for _, pid := range pids {
		found, err := unminimizeIn(pid, title)
		if found {
			if err != nil {
				return err
			}
			_ = exec.Command("open", "-a", appName).Run()
			return nil
		}
	}
	return notFound(fmt.Sprintf("minimized window %q of %q (other-Space windows are invisible to kAXWindows)", title, appName))
}

// unminimizeIn scans one pid's kAXWindows for an exact-titled minimized
// window; found reports whether one matched (err then says how the
// unminimize went). Attribute failures skip the candidate: another pid
// of the same app may still hold the window.
func unminimizeIn(pid int, title string) (found bool, err error) {
	app := C.AXUIElementCreateApplication(C.pid_t(pid))
	defer release(C.CFTypeRef(app))
	wins, werr := arrayAttr(app, attrWindows, "app windows")
	if werr != nil {
		return false, nil
	}
	defer release(C.CFTypeRef(wins))
	for i := 0; i < arrayLen(wins); i++ {
		win := arrayEl(wins, i)
		got, terr := stringAttr(win, attrTitle, "window title")
		if terr != nil || !titleMatches(got, title) {
			continue
		}
		minimized, merr := boolAttr(win, attrMinimized, "window AXMinimized")
		if merr != nil || !minimized {
			continue
		}
		if code := C.AXUIElementSetAttributeValue(win, attrMinimized,
			C.CFTypeRef(C.kCFBooleanFalse)); code != C.kAXErrorSuccess {
			return true, mapAXError("set AXMinimized", int(code))
		}
		still, berr := boolAttr(win, attrMinimized, "AXMinimized readback")
		if berr != nil {
			return true, berr
		}
		if still {
			return true, &AXError{Op: "set AXMinimized (readback still minimized)", Code: 0}
		}
		for range raiseTries {
			if C.AXUIElementPerformAction(win, actionRaise) == C.kAXErrorSuccess {
				break
			}
			time.Sleep(raiseGap)
		}
		return true, nil
	}
	return false, nil
}

// dockPid resolves the Dock's pid via lsappinfo -- no AppleScript, same
// exec pattern as the dockmirror module's other samplers.
func dockPid(ctx context.Context) (int, error) {
	out, err := exec.CommandContext(ctx, "lsappinfo", "info", "-only", "pid", "-app", "com.apple.dock").Output()
	if err != nil {
		return 0, fmt.Errorf("lsappinfo dock pid: %w", err)
	}
	return parsePid(string(out))
}

// parsePid extracts the pid from lsappinfo's `"pid"=34400` shape,
// tolerating spaces around the equals sign.
func parsePid(out string) (int, error) {
	i := strings.Index(out, `"pid"`)
	if i < 0 {
		return 0, fmt.Errorf("ax: no pid in lsappinfo output %q", strings.TrimSpace(out))
	}
	rest := strings.TrimLeft(out[i+len(`"pid"`):], " =")
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 {
		return 0, fmt.Errorf("ax: no pid in lsappinfo output %q", strings.TrimSpace(out))
	}
	return strconv.Atoi(rest[:j])
}

// parseAppPids extracts the pid of every `lsappinfo list` entry whose
// quoted name is exactly app. Entries are multi-line: a numbered header
// carries the name (` 1) "loginwindow" ASN:0x0-0x3003:`), a later detail
// line carries `pid = 446`.
func parseAppPids(lsappinfoOut, app string) []int {
	var pids []int
	current := ""
	for line := range strings.SplitSeq(lsappinfoOut, "\n") {
		t := strings.TrimSpace(line)
		i := 0
		for i < len(t) && t[i] >= '0' && t[i] <= '9' {
			i++
		}
		if i > 0 && i < len(t) && t[i] == ')' {
			current = ""
			if q := strings.Index(t, `"`); q >= 0 {
				if end := strings.Index(t[q+1:], `"`); end >= 0 {
					current = t[q+1 : q+1+end]
				}
			}
			continue
		}
		if current != app || !strings.HasPrefix(t, "pid") {
			continue
		}
		rest := strings.TrimLeft(t[len("pid"):], " =")
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j == 0 {
			continue
		}
		if pid, err := strconv.Atoi(rest[:j]); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// --- CF plumbing ---

func release(v C.CFTypeRef) {
	if v != 0 {
		C.CFRelease(v)
	}
}

// copyAttr copies one AX attribute value; the caller releases it. A
// success that hands back NULL (a known misbehavior of self-implemented
// AX servers in third-party apps) is an error here -- the typed readers
// below would CFGetTypeID(0) and crash.
func copyAttr(el C.AXUIElementRef, attr C.CFStringRef, op string) (C.CFTypeRef, error) {
	var v C.CFTypeRef
	if code := C.AXUIElementCopyAttributeValue(el, attr, &v); code != C.kAXErrorSuccess {
		return 0, mapAXError(op, int(code))
	}
	if v == 0 {
		return 0, &AXError{Op: op + ": success with NULL value", Code: 0}
	}
	return v, nil
}

func stringAttr(el C.AXUIElementRef, attr C.CFStringRef, op string) (string, error) {
	v, err := copyAttr(el, attr, op)
	if err != nil {
		return "", err
	}
	defer release(v)
	if C.CFGetTypeID(v) != C.CFStringGetTypeID() {
		return "", &AXError{Op: op + ": non-string value", Code: 0}
	}
	return goString(C.CFStringRef(v)), nil
}

func boolAttr(el C.AXUIElementRef, attr C.CFStringRef, op string) (bool, error) {
	v, err := copyAttr(el, attr, op)
	if err != nil {
		return false, err
	}
	defer release(v)
	if C.CFGetTypeID(v) != C.CFBooleanGetTypeID() {
		return false, &AXError{Op: op + ": non-boolean value", Code: 0}
	}
	return C.CFBooleanGetValue(C.CFBooleanRef(v)) != 0, nil
}

func arrayAttr(el C.AXUIElementRef, attr C.CFStringRef, op string) (C.CFArrayRef, error) {
	v, err := copyAttr(el, attr, op)
	if err != nil {
		return 0, err
	}
	if C.CFGetTypeID(v) != C.CFArrayGetTypeID() {
		release(v)
		return 0, &AXError{Op: op + ": non-array value", Code: 0}
	}
	return C.CFArrayRef(v), nil
}

func arrayLen(arr C.CFArrayRef) int { return int(C.CFArrayGetCount(arr)) }

func arrayEl(arr C.CFArrayRef, i int) C.AXUIElementRef {
	return C.AXUIElementRef(C.CFArrayGetValueAtIndex(arr, C.CFIndex(i)))
}

// goString copies a CFString into a Go string.
func goString(s C.CFStringRef) string {
	if s == 0 {
		return ""
	}
	if p := C.CFStringGetCStringPtr(s, C.kCFStringEncodingUTF8); p != nil {
		return C.GoString(p)
	}
	n := C.CFStringGetMaximumSizeForEncoding(C.CFStringGetLength(s), C.kCFStringEncodingUTF8) + 1
	buf := make([]byte, int(n))
	if C.CFStringGetCString(s, (*C.char)(unsafe.Pointer(&buf[0])), n, C.kCFStringEncodingUTF8) == 0 {
		return ""
	}
	if i := bytes.IndexByte(buf, 0); i >= 0 {
		buf = buf[:i]
	}
	return string(buf)
}
