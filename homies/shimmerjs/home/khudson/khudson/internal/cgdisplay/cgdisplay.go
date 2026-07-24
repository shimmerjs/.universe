// Package cgdisplay enumerates active CoreGraphics displays locally.
// The reno scene builder selects SCK capture displays by CGDisplay UUID
// -- enumerating them through obs-websocket instead
// (GetInputPropertiesListPropertyItems on the screen_capture source's
// display_uuid list) SEGVs obs-websocket in OBS 32.2 (strlen(NULL),
// crash-verified 2026-07-23), and khudson always runs on the machine
// OBS captures, so local CG is both safer and simpler. Needs no TCC:
// display lists and geometry are unrestricted.
package cgdisplay

/*
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation
#include <CoreGraphics/CoreGraphics.h>
#include <CoreFoundation/CoreFoundation.h>

// exported by CoreGraphics but absent from current SDK headers (OBS's
// mac-capture links the same symbol)
extern CFUUIDRef CGDisplayCreateUUIDFromDisplayID(uint32_t displayID);

static int activeDisplays(uint32_t *ids, int max) {
	uint32_t count = 0;
	if (CGGetActiveDisplayList((uint32_t)max, ids, &count) != kCGErrorSuccess) {
		return -1;
	}
	return (int)count;
}

static void displayUUID(uint32_t id, char *buf, int len) {
	buf[0] = 0;
	CFUUIDRef u = CGDisplayCreateUUIDFromDisplayID(id);
	if (u == NULL) {
		return;
	}
	CFStringRef s = CFUUIDCreateString(NULL, u);
	if (s != NULL) {
		CFStringGetCString(s, buf, len, kCFStringEncodingUTF8);
		CFRelease(s);
	}
	CFRelease(u);
}

static void displayBounds(uint32_t id, int *x, int *y, int *w, int *h) {
	CGRect r = CGDisplayBounds(id);
	*x = (int)r.origin.x;
	*y = (int)r.origin.y;
	*w = (int)r.size.width;
	*h = (int)r.size.height;
}

static int displayIsMain(uint32_t id) { return CGDisplayIsMain(id); }

// pixel dimensions: CGDisplayBounds is in points; capture sources see
// pixels
static void displayPixels(uint32_t id, int *w, int *h) {
	*w = (int)CGDisplayPixelsWide(id);
	*h = (int)CGDisplayPixelsHigh(id);
}
*/
import "C"

// Display is one active display: the CGDirectDisplayID (the legacy
// display_capture selector), its UUID (the SCK selector), point origin
// and size, and pixel size (crop math space).
type Display struct {
	ID         uint32
	UUID       string
	X, Y, W, H int
	PxW, PxH   int
	Main       bool
}

// Active lists the active displays, main display first as CG orders it.
func Active() []Display {
	const maxDisplays = 16
	var ids [maxDisplays]C.uint32_t
	n := int(C.activeDisplays(&ids[0], maxDisplays))
	if n <= 0 {
		return nil
	}
	out := make([]Display, 0, n)
	for i := range n {
		id := ids[i]
		var buf [64]C.char
		C.displayUUID(id, &buf[0], 64)
		var x, y, w, h, pw, ph C.int
		C.displayBounds(id, &x, &y, &w, &h)
		C.displayPixels(id, &pw, &ph)
		out = append(out, Display{
			ID:   uint32(id),
			UUID: C.GoString(&buf[0]),
			X:    int(x), Y: int(y), W: int(w), H: int(h),
			PxW: int(pw), PxH: int(ph),
			Main: C.displayIsMain(id) != 0,
		})
	}
	return out
}
