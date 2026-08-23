package reno

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// RecordConfig is one recording's target configuration -- the one core
// every surface drives (script headers, `khudson reno config`, recorder
// view menus). The zero value means "OBS as configured": the record verb
// touches nothing but the record directory and the optional scene
// header. Any set knob makes the verb ensure the khudson-owned scene
// matches and switch to it.
type RecordConfig struct {
	// Screens selects the display(s) to capture, in canvas order.
	Screens []ScreenSel `json:"screens,omitempty"`
	// Audio is the input device query: "" leaves OBS audio alone,
	// AudioNone mutes every non-reno audio input, anything else resolves
	// against the enumerated device items (UID or unique name substring).
	Audio string `json:"audio,omitempty"`
	// Webcam is a placement preset from WebcamPresets; "" means no
	// webcam item.
	Webcam string `json:"webcam,omitempty"`
}

// AudioNone records with no microphone: the plan mutes every audio
// input reno does not own.
const AudioNone = "none"

// Empty reports the zero config: record OBS exactly as it stands.
func (c RecordConfig) Empty() bool {
	return len(c.Screens) == 0 && c.Audio == "" && c.Webcam == ""
}

// Summary is the one-line human form for status rows and events.
func (c RecordConfig) Summary() string {
	if c.Empty() {
		return "obs as-is"
	}
	var parts []string
	for _, s := range c.Screens {
		parts = append(parts, "screen "+s.String())
	}
	if c.Audio != "" {
		parts = append(parts, "audio "+c.Audio)
	}
	if c.Webcam != "" {
		parts = append(parts, "webcam "+c.Webcam)
	}
	return strings.Join(parts, ", ")
}

// ScreenSel is one captured display plus an optional region crop in
// display pixels.
type ScreenSel struct {
	// Display resolves against the enumerated display items: a 1-based
	// monitor index or a unique name substring.
	Display string  `json:"display"`
	Region  *Region `json:"region,omitempty"`
}

func (s ScreenSel) String() string {
	if s.Region == nil {
		return s.Display
	}
	return s.Display + ":" + s.Region.String()
}

// Region is a crop rectangle in source pixels.
type Region struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

func (r Region) String() string {
	return fmt.Sprintf("%d,%d,%dx%d", r.X, r.Y, r.W, r.H)
}

var regionRE = regexp.MustCompile(`^(\d+),(\d+),(\d+)x(\d+)$`)

// ParseRegion parses the script/CLI region syntax `x,y,WxH`.
func ParseRegion(s string) (*Region, error) {
	m := regionRE.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("region %q must be x,y,WxH", s)
	}
	var v [4]int
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	if v[2] == 0 || v[3] == 0 {
		return nil, fmt.Errorf("region %q has a zero dimension", s)
	}
	return &Region{X: v[0], Y: v[1], W: v[2], H: v[3]}, nil
}

// ParseScreenSel parses the CLI screen syntax `<display>[:x,y,WxH]`.
func ParseScreenSel(s string) (ScreenSel, error) {
	disp, reg, ok := strings.Cut(s, ":")
	if disp == "" {
		return ScreenSel{}, fmt.Errorf("screen %q: empty display selector", s)
	}
	sel := ScreenSel{Display: disp}
	if ok {
		r, err := ParseRegion(reg)
		if err != nil {
			return ScreenSel{}, err
		}
		sel.Region = r
	}
	return sel, nil
}

// WebcamPresets is the ad hoc placement vocabulary: a corner at either
// size, or big. A "+" suffix is the same corner at quarter-canvas size --
// br+ is br, grown. "off" keeps the reno-cam item disabled (a loaded
// preset can be turned off without forgetting the device).
var WebcamPresets = []string{"off", "br", "tr", "bl", "tl", "br+", "tr+", "bl+", "tl+", "big"}

// A placement is a CORNER and a SIZE, and the pad drives them separately:
// four presses arranged where they point, and one pair that grows or
// shrinks whichever corner the camera is in. The preset string is still
// the single name they compose to, because that is what the scene, the
// script and the config all speak.
const (
	// CamBig is the placement that is not a corner: full canvas, so
	// neither the d-pad nor the size pair has anything to say about it.
	CamBig = "big"
	// CamGrown is the suffix marking a corner at quarter-canvas size.
	CamGrown = "+"
)

// CamCorners is the corner vocabulary in D-PAD ORDER -- top-left,
// top-right, bottom-left, bottom-right -- which is reading order and also
// where each one sits on glass. Nothing may reorder this: the renderer
// puts Ends[0] in the top left.
var CamCorners = []string{"tl", "tr", "bl", "br"}

// SplitPlacement takes a preset apart into its corner and whether it is
// grown. A placement that is not a corner (off, big, unset) reports no
// corner, which is what dims the size pair -- there is nothing to resize.
func SplitPlacement(preset string) (corner string, grown bool) {
	corner, grown = strings.TrimSuffix(preset, CamGrown), strings.HasSuffix(preset, CamGrown)
	if !slices.Contains(CamCorners, corner) {
		return "", false
	}
	return corner, grown
}

// Placement composes a corner and a size back into a preset name.
func Placement(corner string, grown bool) string {
	if grown {
		return corner + CamGrown
	}
	return corner
}

// CheckWebcam vets a webcam preset.
func CheckWebcam(p string) error {
	if !slices.Contains(WebcamPresets, p) {
		return fmt.Errorf("webcam %q wants one of %s", p, strings.Join(WebcamPresets, "|"))
	}
	return nil
}

// PropertyItem is one enumerated OBS property list item (display,
// microphone, camera): the human label and the settings value,
// stringified for the wire (legacy display indices arrive numeric).
type PropertyItem struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Monitor mirrors the GetMonitorList entry the plan needs for labels
// and geometry fallbacks.
type Monitor struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// ObsInventory is the probe verb's device enumeration, noted into the
// module over ctl so the recorder view can build menus without ever
// dialing OBS itself.
type ObsInventory struct {
	At         time.Time      `json:"at"`
	ObsVersion string         `json:"obsVersion,omitempty"`
	Screens    []PropertyItem `json:"screens,omitempty"`
	Mics       []PropertyItem `json:"mics,omitempty"`
	Cams       []PropertyItem `json:"cams,omitempty"`
	Monitors   []Monitor      `json:"monitors,omitempty"`
}

// ResolveItem resolves a query against enumerated items: exact value,
// exact name, 1-based index, else unique name substring
// (case-insensitive). Zero and ambiguous matches fail loudly -- the
// handle discipline.
func ResolveItem(items []PropertyItem, query, what string) (PropertyItem, error) {
	for _, it := range items {
		if it.Value == query || it.Name == query {
			return it, nil
		}
	}
	if n, err := strconv.Atoi(query); err == nil && n >= 1 && n <= len(items) {
		return items[n-1], nil
	}
	var hits []PropertyItem
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.Name), strings.ToLower(query)) {
			hits = append(hits, it)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return PropertyItem{}, fmt.Errorf("%s %q matches nothing (have: %s)", what, query, itemNames(items))
	}
	return PropertyItem{}, fmt.Errorf("%s %q is ambiguous: %s", what, query, itemNames(hits))
}

func itemNames(items []PropertyItem) string {
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = it.Name
	}
	return strings.Join(names, ", ")
}

// Source names reno owns inside OBS. Everything the plan creates is
// prefixed reno-; nothing else is ever mutated except audio mutes under
// AudioNone (khudson-managed OBS by decision).
const (
	SceneName  = "reno"
	MicInput   = "reno-mic"
	CamInput   = "reno-cam"
	screenStem = "reno-screen-"
)

// ScreenInputName names the capture input for canvas slot i.
func ScreenInputName(i int) string { return screenStem + strconv.Itoa(i) }

// IsScreenInput reports whether name is a reno screen input (prune
// candidates).
func IsScreenInput(name string) bool { return strings.HasPrefix(name, screenStem) }

// IsRenoInput reports whether an input is one khudson created. The release
// path disables only these: a source someone added to the reno scene by
// hand is theirs, and a verb that reached past its own inputs would be
// editing a scene it does not own.
func IsRenoInput(name string) bool {
	return name == MicInput || name == CamInput || IsScreenInput(name)
}

// ObsEnv is the probed OBS environment a plan is built against. The
// verb gathers it fresh at apply time; nothing is trusted from an old
// inventory.
type ObsEnv struct {
	CanvasW, CanvasH float64
	// FPS is the canvas frame rate; FX sleeps are denominated in frames.
	FPS float64
	// ScreenKind/CamKind are the capture input kinds picked from
	// GetInputKindList (screen_capture over display_capture,
	// macos-avcapture over av_capture_input).
	ScreenKind string
	CamKind    string
	// DisplayProp is the screen kind's display-selection property
	// ("display_uuid" for SCK, "display" legacy); DisplayNumeric marks
	// the legacy int-valued form.
	DisplayProp    string
	DisplayNumeric bool
	CamProp        string
	Displays       []PropertyItem
	Mics           []PropertyItem
	Cams           []PropertyItem
	Monitors       []Monitor
	// Scenes and Inputs are current OBS state: scene names, and every
	// input's name -> kind.
	Scenes []string
	Inputs map[string]string
}

// PickKind returns the first of prefs present in kinds.
func PickKind(kinds []string, prefs ...string) string {
	for _, p := range prefs {
		if slices.Contains(kinds, p) {
			return p
		}
	}
	return ""
}

// PlanInput is one input the plan ensures: created under SceneName when
// absent, settings applied when it exists.
type PlanInput struct {
	Name     string
	Kind     string
	Settings map[string]any
	Enabled  bool
	// Slot/Region drive the transform: the executor reads every screen's
	// source dimensions after settings land, lays the slots out with
	// StackBoxes, and places each one with ItemTransform.
	Slot   int
	Slots  int
	Region *Region
	// Preset is the webcam placement ("" for screen items).
	Preset string
	// Monitor carries the resolved display's geometry when known (the
	// source-dimension fallback for region math).
	Monitor *Monitor
}

// Plan is the desired reno scene state BuildScenePlan computes and the
// record verb executes: pure data, unit-testable without OBS.
type Plan struct {
	Scene        string
	CreateScene  bool
	Inputs       []PlanInput
	RemoveInputs []string
	// Stack is the enabled VISUAL items bottom-first: screens, then the
	// webcam over them. OBS renders by item index and nothing here used to
	// set one, so the stack was whatever creation order left behind -- a
	// screen capture recreated after the cam covers the whole canvas and
	// the PiP disappears with the cam source healthy, its transform
	// correct, and every readout ok (glass, 2026-08-22). Audio items never
	// enter: they have no geometry to be occluded.
	Stack []string
	// Mutes maps audio input name -> muted. Under AudioNone every
	// non-reno audio input mutes; with a device selected they are left
	// alone (the khudson mic is additive); with no audio knob only the
	// reno mic mutes (a prior take must not stay hot).
	Mutes map[string]bool
}

// BuildScenePlan computes the desired scene state for cfg against env.
// Only called for non-empty configs.
func BuildScenePlan(cfg RecordConfig, env ObsEnv) (*Plan, error) {
	if cfg.Empty() {
		return nil, errors.New("empty record config plans nothing")
	}
	p := &Plan{Scene: SceneName, Mutes: map[string]bool{}}
	if !slices.Contains(env.Scenes, SceneName) {
		p.CreateScene = true
	}

	if len(cfg.Screens) > 0 && env.ScreenKind == "" {
		return nil, errors.New("no screen capture input kind available (screen_capture/display_capture)")
	}
	for i, sel := range cfg.Screens {
		it, err := ResolveItem(env.Displays, sel.Display, "display")
		if err != nil {
			return nil, err
		}
		var val any = it.Value
		if env.DisplayNumeric {
			n, err := strconv.Atoi(it.Value)
			if err != nil {
				return nil, fmt.Errorf("display %q: legacy index %q is not numeric", sel.Display, it.Value)
			}
			val = n
		}
		p.Inputs = append(p.Inputs, PlanInput{
			Name:     ScreenInputName(i),
			Kind:     env.ScreenKind,
			Settings: map[string]any{env.DisplayProp: val, "show_cursor": true},
			Enabled:  true,
			Slot:     i,
			Slots:    len(cfg.Screens),
			Region:   sel.Region,
			Monitor:  monitorByName(env.Monitors, it.Name),
		})
		p.Stack = append(p.Stack, ScreenInputName(i))
	}
	// stale screen slots from a wider previous config get removed
	for name, kind := range env.Inputs {
		if IsScreenInput(name) && kind == env.ScreenKind && !hasInput(p.Inputs, name) {
			p.RemoveInputs = append(p.RemoveInputs, name)
		}
	}

	switch cfg.Audio {
	case "":
		// leave OBS audio alone -- except reno's own mic: a prior take's
		// device selection must not stay hot into a config that never
		// asked for audio (arc-2 review #17)
		if _, ok := env.Inputs[MicInput]; ok {
			p.Mutes[MicInput] = true
		}
	case AudioNone:
		for name, kind := range env.Inputs {
			if isAudioKind(kind) && name != MicInput {
				p.Mutes[name] = true
			}
		}
		if _, ok := env.Inputs[MicInput]; ok {
			p.Mutes[MicInput] = true
		}
	default:
		it, err := ResolveItem(env.Mics, cfg.Audio, "audio device")
		if err != nil {
			return nil, err
		}
		p.Inputs = append(p.Inputs, PlanInput{
			Name:     MicInput,
			Kind:     "coreaudio_input_capture",
			Settings: map[string]any{"device_id": it.Value},
			Enabled:  true,
		})
		p.Mutes[MicInput] = false
	}

	switch {
	case cfg.Webcam != "":
		if err := CheckWebcam(cfg.Webcam); err != nil {
			return nil, err
		}
		if env.CamKind == "" {
			return nil, errors.New("no camera input kind available (macos-avcapture/av_capture_input)")
		}
		in := PlanInput{
			Name:    CamInput,
			Kind:    env.CamKind,
			Enabled: cfg.Webcam != "off",
			Preset:  cfg.Webcam,
		}
		if len(env.Cams) > 0 {
			// default device: first enumerated; a query knob can come later
			in.Settings = map[string]any{env.CamProp: env.Cams[0].Value}
		}
		p.Inputs = append(p.Inputs, in)
		if in.Enabled {
			p.Stack = append(p.Stack, CamInput)
		}
	default:
		// Webcam=="" means NO webcam item: a prior take's still-enabled
		// reno-cam must not ride into this recording (arc-2 review #5 --
		// the stale-camera mirror of the stale-screen prune)
		if kind, ok := env.Inputs[CamInput]; ok {
			p.Inputs = append(p.Inputs, PlanInput{Name: CamInput, Kind: kind, Enabled: false})
		}
	}
	return p, nil
}

func monitorByName(ms []Monitor, name string) *Monitor {
	for i := range ms {
		if ms[i].Name == name {
			return &ms[i]
		}
	}
	return nil
}

func hasInput(ins []PlanInput, name string) bool {
	for _, in := range ins {
		if in.Name == name {
			return true
		}
	}
	return false
}

// isAudioKind marks inputs the AudioNone mute sweep covers.
func isAudioKind(kind string) bool {
	return strings.Contains(kind, "audio")
}

// Transform is the scene-item transform subset the plan sets: top-left
// anchored bounds box (scale-inner keeps aspect) plus source crops.
type Transform struct {
	X, Y, BoundsW, BoundsH     float64
	CropL, CropT, CropR, CropB int
	// ScaleX/ScaleY are sent explicitly, even though a scale-inner bounds
	// box is supposed to derive them. OBS derives the scale ONCE, when the
	// bounds are set; an item given bounds while its source still reads 0x0
	// gets scale 0 and keeps it after frames arrive, rendering at zero size
	// forever. The scene then composites to black with every source in it
	// healthy, which is exactly what it looks like: nothing. Zero here means
	// "no live source size to derive from", and the field is omitted.
	ScaleX, ScaleY float64
}

// ScaleInner is the scale a SCALE_INNER bounds box implies for a source of
// srcW x srcH: fit inside the box, preserve aspect. Zero for a source that
// is not reporting a size yet -- there is nothing honest to compute.
func ScaleInner(boundsW, boundsH float64, srcW, srcH int) (float64, float64) {
	if srcW <= 0 || srcH <= 0 || boundsW <= 0 || boundsH <= 0 {
		return 0, 0
	}
	s := min(boundsW/float64(srcW), boundsH/float64(srcH))
	return s, s
}

// EffectiveSize is the source size the canvas actually sees: the region
// crop when one is set against a source reporting real dimensions, else
// the source itself. Both the row heights and the scale are denominated
// in it -- a cropped source occupies its crop, not its full sensor.
func EffectiveSize(srcW, srcH int, region *Region) (int, int) {
	if region == nil || srcW <= 0 || srcH <= 0 {
		return srcW, srcH
	}
	r := clampRegion(*region, srcW, srcH)
	return r.W, r.H
}

// StackGutterFrac is the gap between stacked rows as a fraction of canvas
// WIDTH. Two captures butted edge to edge share an invisible seam -- the
// desktop just runs on into the strip below it -- so the rows are held
// apart far enough to read as two screens. Width, not height, because the
// canvas height is now derived FROM the stack (StackCanvas) and a gutter
// denominated in it would not have a value to be a fraction of yet.
//
// A sixty-fourth lands on whole pixels for every canvas width this records.
const StackGutterFrac = 1.0 / 64

// StackCanvas is the canvas a stack of screens wants: wide enough that
// nothing has to be UPSCALED, and exactly tall enough to hold the rows and
// their gutters. sizes are the screens' effective pixel dimensions in slot
// order. Zero, or any non-positive size, means there is nothing to derive
// from and the caller keeps the canvas it has.
//
// The width is the NARROWEST source, so that one lands at 1:1 and every
// other merely shrinks. That asymmetry is the point: downscaling a desktop
// costs it some sharpness, where upscaling a 2560x720 status strip to 3840
// invents pixels across every glyph edge and turns small text to mush. The
// legible thing wins the pixel grid.
func StackCanvas(sizes [][2]int) (w, h float64, ok bool) {
	if len(sizes) == 0 {
		return 0, 0, false
	}
	cw := 0
	for _, s := range sizes {
		if s[0] <= 0 || s[1] <= 0 {
			return 0, 0, false
		}
		if cw == 0 || s[0] < cw {
			cw = s[0]
		}
	}
	gutter := math.Round(float64(cw) * StackGutterFrac)
	total := gutter * float64(len(sizes)-1)
	for _, s := range sizes {
		total += math.Round(float64(cw) * float64(s[1]) / float64(s[0]))
	}
	return float64(cw), total, true
}

// StackBoxes lays the screen slots out as rows, one under the next, every
// slot at the same width: the widest the whole stack fits the canvas at,
// gutters included. aspects[i] is slot i's effective width/height; a
// non-positive one falls back to the canvas aspect, which is all a source
// not yet reporting a size supports.
//
// Columns wasted width on the displays this records. A 3840x2160 and a
// 2560x720 in half-canvas columns each render 1920 wide; stacked they
// share about 2520, because the stack's limit is canvas HEIGHT and these
// two are far wider than they are tall. The block centers, and nothing is
// cropped to make it fit -- a stack that cannot use the full width leaves
// bars at the sides, which is the honest rendering of a tall composition
// in a wide frame.
func StackBoxes(canvasW, canvasH float64, aspects []float64) []Transform {
	if len(aspects) == 0 || canvasW <= 0 || canvasH <= 0 {
		return nil
	}
	inv := make([]float64, len(aspects))
	var sum float64
	for i, a := range aspects {
		if a <= 0 {
			a = canvasW / canvasH
		}
		inv[i] = 1 / a
		sum += inv[i]
	}
	gutter := 0.0
	if len(aspects) > 1 {
		// width, matching StackCanvas: the canvas height is derived FROM
		// this gutter, so measuring the gutter against the height would be
		// each defining the other
		gutter = math.Round(canvasW * StackGutterFrac)
	}
	gaps := gutter * float64(len(aspects)-1)
	w := min(canvasW, max(canvasH-gaps, 0)/sum)
	x, y := (canvasW-w)/2, (canvasH-(w*sum+gaps))/2
	out := make([]Transform, len(aspects))
	for i, iv := range inv {
		h := w * iv
		out[i] = Transform{X: x, Y: y, BoundsW: w, BoundsH: h}
		y += h + gutter
	}
	return out
}

// ItemTransform places a source in its canvas box and converts an optional
// region into source crops. Crops are in source pixels against srcW/srcH;
// a zero source dimension (capture not yet streaming, e.g. pre-TCC) skips
// the crop rather than producing garbage -- callers surface that loudly.
func ItemTransform(box Transform, srcW, srcH int, region *Region) (Transform, bool) {
	t := box
	if region == nil {
		return t, true
	}
	if srcW <= 0 || srcH <= 0 {
		return t, false
	}
	r := clampRegion(*region, srcW, srcH)
	t.CropL, t.CropT = r.X, r.Y
	t.CropR, t.CropB = srcW-(r.X+r.W), srcH-(r.Y+r.H)
	return t, true
}

// CamTransform places the webcam preset on the canvas. A corner sits at a
// fifth of the canvas per side, or half with a "+" -- half each way is a
// QUARTER of the canvas area, prominent while the screen stays readable
// around it, and it stays pinned to its corner rather than drifting to the
// middle over the content. big is nine tenths, centered, and is the only
// preset that leaves a corner.
// srcW/srcH are the camera's own dimensions; they shape the box so a
// corner preset actually HUGS its corner. A box sized to the canvas
// instead only hugs when the camera happens to share the canvas's aspect,
// which stopped being true the moment the canvas started following the
// screens: a 16:9 camera in a half-of-2560x2200 box floats 380px clear of
// the bottom edge it is named after. Zero means the size is unknown and
// the box falls back to the canvas's own proportions.
func CamTransform(canvasW, canvasH float64, preset string, srcW, srcH int) Transform {
	const pipFrac, quarterFrac, bigFrac, pad = 0.2, 0.5, 0.9, 24.0
	// box sizes a w x h that fits frac of the canvas and keeps the camera's
	// aspect, so the bounds are the picture rather than a rect around it
	box := func(frac float64) (float64, float64) {
		w, h := canvasW*frac, canvasH*frac
		if srcW <= 0 || srcH <= 0 {
			return w, h
		}
		if byW := w * float64(srcH) / float64(srcW); byW <= h {
			return w, byW
		}
		return h * float64(srcW) / float64(srcH), h
	}
	if preset == "big" {
		w, h := box(bigFrac)
		return Transform{X: (canvasW - w) / 2, Y: (canvasH - h) / 2, BoundsW: w, BoundsH: h}
	}
	corner, quarter := strings.CutSuffix(preset, "+")
	switch corner {
	case "tl", "tr", "bl", "br", "off":
		f := pipFrac
		if quarter {
			f = quarterFrac
		}
		w, h := box(f)
		t := Transform{BoundsW: w, BoundsH: h, X: pad, Y: pad}
		if strings.HasSuffix(corner, "r") {
			t.X = canvasW - w - pad
		}
		if strings.HasPrefix(corner, "b") {
			t.Y = canvasH - h - pad
		}
		return t
	}
	return Transform{BoundsW: canvasW, BoundsH: canvasH}
}

func clampRegion(r Region, w, h int) Region {
	r.X = min(max(r.X, 0), w-1)
	r.Y = min(max(r.Y, 0), h-1)
	r.W = min(max(r.W, 1), w-r.X)
	r.H = min(max(r.H, 1), h-r.Y)
	return r
}
