package cgdisplay

/*
#include <CoreGraphics/CoreGraphics.h>
static CGRect topologyBounds(uint32_t id) { return CGDisplayBounds(id); }
*/
import "C"

import (
	"fmt"
	"slices"
	"strings"
)

type topologyAPI struct {
	ids    func() ([]uint32, error)
	bounds func(uint32) (int, int, int, int)
}

// Fingerprint uses only documented display enumeration and bounds APIs. It
// does not resolve UUIDs or copy modes for the launcher's presence check.
func Fingerprint() (string, error) {
	return fingerprint(topologyAPI{
		ids: func() ([]uint32, error) {
			for size := 16; size <= 256; size *= 2 {
				ids := make([]C.CGDirectDisplayID, size)
				var count C.uint32_t
				if code := int32(C.CGGetActiveDisplayList(C.uint32_t(size), &ids[0], &count)); code != 0 {
					return nil, &APIError{Boundary: "enumerate", Code: code}
				}
				if int(count) >= size {
					continue
				}
				out := make([]uint32, int(count))
				for i := range out {
					out[i] = uint32(ids[i])
				}
				return out, nil
			}
			return nil, fmt.Errorf("display enumeration exceeded 256 entries")
		},
		bounds: func(id uint32) (int, int, int, int) {
			r := C.topologyBounds(C.uint32_t(id))
			return int(r.origin.x), int(r.origin.y), int(r.size.width), int(r.size.height)
		},
	})
}
func fingerprint(api topologyAPI) (string, error) {
	ids, err := api.ids()
	if err != nil {
		return "", err
	}
	slices.Sort(ids)
	var out strings.Builder
	for _, id := range ids {
		x, y, w, h := api.bounds(id)
		if w <= 0 || h <= 0 {
			return "", fmt.Errorf("display %d has unavailable bounds", id)
		}
		fmt.Fprintf(&out, "%d:%d,%d,%dx%d;", id, x, y, w, h)
	}
	return out.String(), nil
}
