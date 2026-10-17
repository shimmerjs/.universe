package ax

/*
#include <ApplicationServices/ApplicationServices.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"time"
)

// Focus attribute names, process-lifetime like the ax.go block.
var (
	attrFocusedWindow = cfStr("AXFocusedWindow")
	attrMainWindow    = cfStr("AXMainWindow")
	attrFrontmost     = cfStr("AXFrontmost")
)

// openBundle is the cooperative activation request (`open -b`); seam for
// tests. Sonoma made activation cooperative -- a background daemon
// activating third apps can get refusals and bouncing Dock icons -- so
// FocusApp pairs it with an AX raise under the existing grant.
var openBundle = func(bundleID string) error {
	_, err := commandOutput("open", "-b", bundleID)
	return err
}

// FocusApp requests activation, raises a focused/main/listed window, and checks
// AXFrontmost. It does not switch Spaces. Unavailable windows return ErrNotFound;
// permission and messaging failures retain their native errors.
func FocusApp(bundleID string) error {
	if !Trusted() {
		return ErrUntrusted
	}
	if err := boundMessaging(); err != nil {
		return err
	}
	pids, err := resolveBundlePids(bundleID)
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		return notFound(fmt.Sprintf("running bundle %q", bundleID))
	}
	if err := openBundle(bundleID); err != nil {
		return fmt.Errorf("open -b %s: %w", bundleID, err)
	}
	var lastErr error
	for _, pid := range pids {
		found, err := focusIn(pid)
		if found {
			return err
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return notFound(fmt.Sprintf("raisable window of %q (other-Space windows are invisible to kAXWindows)", bundleID))
}

// focusIn raises one pid's best window and confirms the app went
// frontmost. found=false means this pid offered no window (helper
// processes; try the next pid).
func focusIn(pid int) (found bool, err error) {
	app := C.AXUIElementCreateApplication(C.pid_t(pid))
	defer release(C.CFTypeRef(app))

	win, err := pickWindow(app)
	if err != nil || win == 0 {
		return false, err
	}
	defer release(C.CFTypeRef(win))

	if err := raiseWindow(win); err != nil {
		return true, err
	}
	if code := C.AXUIElementSetAttributeValue(app, attrFrontmost,
		C.CFTypeRef(C.kCFBooleanTrue)); code != C.kAXErrorSuccess {
		return true, mapAXError("set AXFrontmost", int(code))
	}
	// Activation can finish after the request returns.
	var readErr error
	for range raiseTries {
		front, ferr := boolAttr(app, attrFrontmost, "AXFrontmost readback")
		if ferr == nil && front {
			return true, nil
		}
		readErr = ferr
		time.Sleep(raiseGap)
	}
	if readErr != nil {
		return true, readErr
	}
	return true, &AXError{Op: "focus-app (readback not frontmost)", Code: 0}
}

// pickWindow resolves the raise target: the app's focused window, else
// its main window, else the first listed window. The returned element is
// retained; the caller releases it.
func pickWindow(app C.AXUIElementRef) (C.AXUIElementRef, error) {
	for _, attr := range []C.CFStringRef{attrFocusedWindow, attrMainWindow} {
		v, err := copyAttr(app, attr, "focus window")
		if err != nil && !optionalAttribute(err) {
			return 0, err
		}
		if err == nil {
			if C.CFGetTypeID(v) != C.AXUIElementGetTypeID() {
				release(v)
				return 0, &AXError{Op: "focus window: non-element value"}
			}
			return C.AXUIElementRef(v), nil
		}
	}
	wins, err := arrayAttr(app, attrWindows, "app windows")
	if err != nil {
		return 0, err
	}
	defer release(C.CFTypeRef(wins))
	if arrayLen(wins) == 0 {
		return 0, nil
	}
	win := arrayEl(wins, 0)
	C.CFRetain(C.CFTypeRef(win))
	return win, nil
}

// attrHidden is the app-level kAXHidden attribute (cmd-H visibility).
var attrHidden = cfStr("AXHidden")

// SetAppHidden updates every matching process and verifies each readback.
// Partial success returns the failures so callers do not assume convergence.
func SetAppHidden(bundleID string, hidden bool) error {
	if !Trusted() {
		return ErrUntrusted
	}
	if err := boundMessaging(); err != nil {
		return err
	}
	pids, err := resolveBundlePids(bundleID)
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		return notFound(fmt.Sprintf("running bundle %q", bundleID))
	}
	return setHiddenPids(pids, hidden, setHiddenIn)
}

func setHiddenPids(pids []int, hidden bool, set func(int, bool) error) error {
	var failures error
	for _, pid := range pids {
		if err := set(pid, hidden); err != nil {
			failures = errors.Join(failures, fmt.Errorf("pid %d: %w", pid, err))
		}
	}
	return failures
}

func setHiddenIn(pid int, hidden bool) error {
	app := C.AXUIElementCreateApplication(C.pid_t(pid))
	defer release(C.CFTypeRef(app))
	val := C.CFTypeRef(C.kCFBooleanFalse)
	if hidden {
		val = C.CFTypeRef(C.kCFBooleanTrue)
	}
	if code := C.AXUIElementSetAttributeValue(app, attrHidden, val); code != C.kAXErrorSuccess {
		return mapAXError("set AXHidden", int(code))
	}
	still, err := boolAttr(app, attrHidden, "AXHidden readback")
	if err != nil {
		return err
	}
	if still != hidden {
		return &AXError{Op: "set AXHidden (readback unchanged)"}
	}
	return nil
}

// AppHidden returns true only if every matching process is known hidden.
// Any known visible process returns false. An absent bundle is ErrNotFound.
func AppHidden(bundleID string) (bool, error) {
	if !Trusted() {
		return false, ErrUntrusted
	}
	if err := boundMessaging(); err != nil {
		return false, err
	}
	pids, err := resolveBundlePids(bundleID)
	if err != nil {
		return false, err
	}
	if len(pids) == 0 {
		return false, notFound(fmt.Sprintf("running bundle %q", bundleID))
	}
	return hiddenPids(pids, func(pid int) (bool, error) {
		app := C.AXUIElementCreateApplication(C.pid_t(pid))
		defer release(C.CFTypeRef(app))
		return boolAttr(app, attrHidden, "AXHidden")
	})
}

func hiddenPids(pids []int, read func(int) (bool, error)) (bool, error) {
	var failures error
	for _, pid := range pids {
		hidden, err := read(pid)
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("pid %d: %w", pid, err))
			continue
		}
		if !hidden {
			return false, nil
		}
	}
	if failures != nil {
		return false, failures
	}
	return true, nil
}

func raiseWindow(win C.AXUIElementRef) error {
	return retryRaise(func() error {
		code := C.AXUIElementPerformAction(win, actionRaise)
		if code == C.kAXErrorSuccess {
			return nil
		}
		return mapAXError("raise window", int(code))
	}, time.Sleep)
}

func retryRaise(raise func() error, pause func(time.Duration)) error {
	var err error
	for i := range raiseTries {
		err = raise()
		if err == nil {
			return nil
		}
		if i+1 < raiseTries {
			pause(raiseGap)
		}
	}
	return err
}
