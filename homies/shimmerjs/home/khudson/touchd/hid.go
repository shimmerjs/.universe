package main

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/sstallion/go-hid"
)

const (
	edgeVID = 0x27C0
	edgePID = 0x0859

	usagePageDigitizer = 0x0D
	usageTouchScreen   = 0x04
	usagePageDesktop   = 0x01
	usageMouse         = 0x02

	// input report carrying 10 finger slots + scan time + contact count
	reportTouch = 0x0D
	// mouse-collection input report: [buttons][X le16][Y le16][wheel] --
	// what the controller emits in mouse-emulation mode (proven tier 2)
	reportMouse = 0x07
	// feature report: contact count maximum
	reportContactMax = 0x0A
	// feature report: device configuration (usage 0x52 device mode)
	reportDeviceMode = 0x21

	// HID digitizer device mode values (windows precision touch convention)
	modeMouse      = 0x00
	modeMultiTouch = 0x02
)

func enumerate() error {
	fmt.Println("Edge HID collections:")
	if err := hid.Enumerate(edgeVID, edgePID, printCollection); err != nil {
		return err
	}
	fmt.Println("Moonlander HID collections:")
	return hid.Enumerate(moonVID, moonPID, printCollection)
}

func printCollection(info *hid.DeviceInfo) error {
	fmt.Printf("  usage_page=0x%04X usage=0x%04X mfr=%q product=%q path=%s\n",
		info.UsagePage, info.Usage, info.MfrStr, info.ProductStr, info.Path)
	return nil
}

// errAbsent classifies an open failure as "collection not enumerable" --
// the device is unplugged, as opposed to present but unopenable (seized,
// not permitted). The reopen loops key their backoff reset on this class
// flipping, so it must survive every wrap on the enumeration path.
var errAbsent = errors.New("device connected?")

func noCollectionErr(vid, pid, page, usage uint16) error {
	return fmt.Errorf("no %04X:%04X collection with usage_page=0x%02X usage=0x%02X (%w)", vid, pid, page, usage, errAbsent)
}

func findCollection(vid, pid, page, usage uint16) (string, error) {
	var path string
	err := hid.Enumerate(vid, pid, func(info *hid.DeviceInfo) error {
		if info.UsagePage == page && info.Usage == usage && path == "" {
			path = info.Path
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("enumerate: %w", err)
	}
	if path == "" {
		return "", noCollectionErr(vid, pid, page, usage)
	}
	return path, nil
}

// openMu serializes device opens: hidapi's darwin exclusive flag is
// process-global state (device_open_options), so an Edge reopen racing a
// Moonlander open could otherwise inherit the wrong mode.
var openMu sync.Mutex

// openPath opens a HID path with an explicit exclusive/shared mode,
// restoring the process default (exclusive, set by hid.Init) afterwards.
// The Edge digitizer keeps its exclusive (seize) open; the Moonlander
// vendor channel opens shared so Keymapp can coexist.
func openPath(path string, exclusive bool) (*hid.Device, error) {
	openMu.Lock()
	defer openMu.Unlock()
	prev := hid.GetOpenExclusive()
	hid.SetOpenExclusive(exclusive)
	defer hid.SetOpenExclusive(prev)
	return hid.OpenPath(path)
}

// openCollection finds and opens the wanted collection and, for the digitizer
// with mode switching enabled, asserts multi-input mode. Mode must be asserted
// on EVERY open: the controller reverts to mouse emulation on re-enumeration
// (unplug, sleep). Returns whether multi-input mode was asserted.
func openCollection(mouse, noMode, verbose bool) (*hid.Device, bool, error) {
	wantPage, wantUsage := uint16(usagePageDigitizer), uint16(usageTouchScreen)
	if mouse {
		wantPage, wantUsage = usagePageDesktop, usageMouse
	}

	path, err := findCollection(edgeVID, edgePID, wantPage, wantUsage)
	if err != nil {
		return nil, false, err
	}
	if verbose {
		fmt.Printf("opening usage_page=0x%02X usage=0x%02X at %s\n", wantPage, wantUsage, path)
	}

	dev, err := openPath(path, true)
	if err != nil {
		return nil, false, fmt.Errorf("open (Input Monitoring granted?): %w", err)
	}
	if mouse {
		return dev, false, nil
	}

	if verbose {
		reportContactCountMax(dev)
	}
	if noMode {
		return dev, false, nil
	}

	if verbose {
		dumpMode(dev, "device mode before")
	}
	if err := setMode(dev, modeMultiTouch); err != nil {
		fmt.Fprintf(os.Stderr, "MODE SWITCH REJECTED: %v -- streaming whatever the device sends\n", err)
		return dev, false, nil
	}
	if verbose {
		fmt.Println("mode switch sent: device mode = multi-input")
		dumpMode(dev, "device mode after")
	}
	return dev, true, nil
}

// setMode writes the device-mode feature report: usage 0x52 device mode =
// value, usage 0x53 device identifier = 0.
func setMode(dev *hid.Device, value byte) error {
	_, err := dev.SendFeatureReport([]byte{reportDeviceMode, value, 0x00})
	return err
}

// deassertMode returns the controller to mouse emulation so the fallback
// driver path works without an unplug cycle.
func deassertMode(dev *hid.Device) {
	if err := setMode(dev, modeMouse); err != nil {
		fmt.Fprintln(os.Stderr, "mode de-assert failed:", err)
		return
	}
	fmt.Println("device mode de-asserted: mouse emulation")
}

func dumpMode(dev *hid.Device, label string) {
	buf := make([]byte, 4)
	buf[0] = reportDeviceMode
	if n, err := dev.GetFeatureReport(buf); err == nil {
		fmt.Printf("%s: %X\n", label, buf[:n])
	}
}

// reportContactCountMax reads feature report 0x0A (Contact Count Maximum).
func reportContactCountMax(dev *hid.Device) {
	buf := make([]byte, 8)
	buf[0] = reportContactMax
	if n, err := dev.GetFeatureReport(buf); err != nil {
		fmt.Printf("contact-count-max feature read failed (non-fatal): %v\n", err)
	} else {
		fmt.Printf("contact count maximum: %d (raw %X)\n", buf[1]&0x0F, buf[:n])
	}
}
