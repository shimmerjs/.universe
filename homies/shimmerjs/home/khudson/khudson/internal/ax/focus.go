package ax

/*
#include <ApplicationServices/ApplicationServices.h>
*/
import "C"

import (
	"fmt"
	"os/exec"
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
	return exec.Command("open", "-b", bundleID).Run()
}

// FocusApp brings the app owning bundleID to the front: cooperative
// activation plus an AX raise of its focused/main window, with a
// frontmost readback as the post-condition (attribute writes silently
// no-op; only the readback proves the focus landed). Window selection:
// AXFocusedWindow, else AXMainWindow, else the first of kAXWindows.
// Space-blind by construction: kAXWindows omits other-Space windows, so
// an app living entirely on another Space comes back ErrNotFound --
// P0 ships no Space switching, and this verb says so rather than
// pretending.
func FocusApp(bundleID string) error {
	if !Trusted() {
		return ErrUntrusted
	}
	boundMessaging()
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

	win, ok := pickWindow(app)
	if !ok {
		return false, nil
	}
	defer release(C.CFTypeRef(win))

	for range raiseTries {
		if C.AXUIElementPerformAction(win, actionRaise) == C.kAXErrorSuccess {
			break
		}
		time.Sleep(raiseGap)
	}
	if code := C.AXUIElementSetAttributeValue(app, attrFrontmost,
		C.CFTypeRef(C.kCFBooleanTrue)); code != C.kAXErrorSuccess {
		return true, mapAXError("set AXFrontmost", int(code))
	}
	// activation animates; retry the readback through it
	for range raiseTries {
		front, ferr := boolAttr(app, attrFrontmost, "AXFrontmost readback")
		if ferr == nil && front {
			return true, nil
		}
		time.Sleep(raiseGap)
	}
	return true, &AXError{Op: "focus-app (readback not frontmost)", Code: 0}
}

// pickWindow resolves the raise target: the app's focused window, else
// its main window, else the first listed window. The returned element is
// retained; the caller releases it.
func pickWindow(app C.AXUIElementRef) (C.AXUIElementRef, bool) {
	for _, attr := range []C.CFStringRef{attrFocusedWindow, attrMainWindow} {
		if v, err := copyAttr(app, attr, "focus window"); err == nil {
			return C.AXUIElementRef(v), true
		}
	}
	wins, err := arrayAttr(app, attrWindows, "app windows")
	if err != nil {
		return 0, false
	}
	defer release(C.CFTypeRef(wins))
	if arrayLen(wins) == 0 {
		return 0, false
	}
	win := arrayEl(wins, 0)
	C.CFRetain(C.CFTypeRef(win))
	return win, true
}
