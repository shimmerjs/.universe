// Webcam FX: the moving half of the cam vocabulary, plugin-free by
// construction. WebcamPresets/CamTransform place the reno-cam item;
// these effects animate it -- instant jumps, visibility, eased paths,
// and a pixelate built on OBS's own scaling filter.
//
// A path is SAMPLED here into an ordered obs-websocket request sequence
// rather than executed as N round trips: the executor ships the whole
// sequence as one SERIAL_FRAME batch, which OBS runs a request per
// rendered frame, so Sleep pseudo-requests pace the easing against the
// video clock instead of websocket jitter. That is the substitute for
// the exeldro Move filter the design names -- nix owns no OBS plugin on
// this machine, so nothing here may depend on one.
package reno

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The closed FX vocabulary, shared by the script command vet, the CLI,
// and the recorder pad's tiles.
const (
	FXMove     = "move"
	FXJump     = "jump"
	FXShow     = "show"
	FXHide     = "hide"
	FXPixelate = "pixelate"
	FXPurple   = "purple"
	FXJitter   = "jitter"
	FXLava     = "lava"
)

// CamFXKinds is the vocabulary in error-message order.
var CamFXKinds = []string{FXMove, FXJump, FXShow, FXHide, FXPixelate, FXPurple, FXJitter, FXLava}

// LevelFXKinds are the magnitude effects: one integer, 0 for off, walked
// by a plus/minus pair on the pad instead of flipped by a toggle. Nothing
// downstream reports the magnitude back -- a filter tells you its settings
// but not which step of khudson's scale produced them, and a shake leaves
// no trace at all -- so the level has to be state khudson holds, which is
// exactly why a tile may show it.
var LevelFXKinds = []string{FXPixelate, FXPurple, FXJitter, FXLava}

// IsLevelFX reports whether kind is a magnitude effect.
func IsLevelFX(kind string) bool { return slices.Contains(LevelFXKinds, kind) }

// SustainFXKinds are the level effects that MOVE: they run for as long as
// their level is up, so a level above zero is not a request, it is a state
// somebody has to keep driving. What drives them is one long-lived verb per
// effect (SustainSetup / SustainChunk / SustainHome), not a re-fired
// one-shot -- see FXSustainChunk.
var SustainFXKinds = []string{FXJitter, FXLava}

// IsSustainFX reports whether kind has to be driven while it is up.
func IsSustainFX(kind string) bool { return slices.Contains(SustainFXKinds, kind) }

// IsFilterFX reports whether kind lands in a source filter, and so needs
// the filter capability probe in front of it. Jitter is the one level
// effect that is pure motion, and probing filters for it would be a round
// trip spent on nothing.
func IsFilterFX(kind string) bool {
	return kind == FXPixelate || kind == FXPurple || kind == FXLava
}

// FXEases names the interpolation curves a move may take.
var FXEases = []string{"linear", "smooth", "bounce"}

// FX defaults and bounds. The duration cap keeps a batch well inside the
// verb watchdog; the sample cap keeps the request count sane on a long
// path -- past it the samples stride over more frames each instead of
// multiplying.
const (
	FXDefaultDur  = 500 * time.Millisecond
	FXDefaultEase = "smooth"
	FXMaxDur      = 10 * time.Second
	FXMaxSamples  = 90
	// FXFallbackFPS stands in when OBS reports no frame rate: every
	// sleep is denominated in frames, so a wrong divisor only mis-paces
	// a move, it never breaks one.
	FXFallbackFPS = 60.0
	// FXMaxLevel is the top of every level effect's range and
	// FXDefaultLevel where a bare "on" lands -- mid-scale, so the first
	// tap is visible without being the whole effect. The scale is fine
	// rather than coarse: the endpoints of each effect are unchanged, a
	// step is just a smaller bite of the same range, so a level can be
	// dialled to taste mid-demo instead of jumping.
	FXMaxLevel     = 20
	FXDefaultLevel = 10
	// FXJitterHz is how fast a shake oscillates. Slower than a one-shot
	// buzz, because this one SUSTAINS: a 12Hz vibration held for a minute
	// reads as a broken capture, where a slow wobble reads as a handheld
	// camera.
	FXJitterHz = 6.0
	// FXJitterMaxFrac is the peak offset at FXMaxLevel, as a fraction of
	// the item's box width. A tenth of the box is plenty on a corner PiP
	// and still inside the canvas.
	FXJitterMaxFrac = 0.10
	// FXLavaHz is how fast the lamp drifts through its colour. Two orders
	// of magnitude under the shake, because the whole read of a lava lamp
	// is that you cannot catch it moving: one full wander every eight
	// seconds is slow enough to look like a light source and fast enough
	// that a demo does not have to wait for it.
	FXLavaHz = 0.125
	// FXLavaMaxShift is the peak hue rotation at FXMaxLevel, in degrees.
	// A quarter turn either way: past that the camera passes back through
	// skin tones on the far side and the drift reads as a glitch rather
	// than a light.
	FXLavaMaxShift = 90.0
	// FXLavaSat is the saturation the lamp holds while it is up. Above 1
	// so the colour it drifts through is a colour and not a tint, and
	// scaled by the level like everything else.
	FXLavaSat = 1.6

	// FXSustainChunk is one batch of a sustained effect. OBS runs one
	// SERIAL_FRAME batch at a time, so an effect that outlives a batch is
	// that batch again -- and the chunk length IS the off latency, because
	// the driver only gets to notice the level dropped between chunks.
	//
	// It is this short because the driver holds the socket open across
	// chunks (SustainChunk, one long-lived verb) instead of spawning a
	// process and dialling per chunk. That was the old cost that forced a
	// 3s chunk and a 3s wait on every "off"; what is left between chunks
	// now is one batch round trip on loopback.
	FXSustainChunk = 250 * time.Millisecond
	// FXSustainOneShot is what a sustained effect does when nobody is
	// driving it: a bare `khudson reno obs cam jitter 8` with no bus to
	// pick the shake up. Long enough to be an effect you can see rather
	// than a blip, and it ends on the pose it started from.
	FXSustainOneShot = 2 * time.Second
	// FXSustainSamples caps the poses in one chunk. A guard rather than a
	// budget: at a plausible frame rate a chunk is well under it, and the
	// cap only bites if OBS reports something absurd.
	FXSustainSamples = 60
)

// The stock filters the level effects drive, and the khudson-owned filter
// names they live under on reno-cam. Both settings schemas are confirmed
// against live OBS 32.2 (GetSourceFilterDefaultSettings): scale_filter
// takes resolution/sampling/undistort, color_filter_v2 takes hue_shift,
// saturation, brightness, contrast, gamma, opacity, color_multiply and
// color_add.
//
// The design's composite-blur path is NOT built -- it is a hand-installed
// plugin under a self-updating cask, and a settings schema nobody here can
// verify is worse than an honest refusal -- so a level effect fails loudly
// when its stock kind is absent.
const (
	FXFilterKind       = "scale_filter"
	FXPixelateDownName = "reno-pixelate"
	FXPixelateUpName   = "reno-pixelate-up"

	FXPurpleFilterKind = "color_filter_v2"
	FXPurpleFilterName = "reno-purple"

	// The lamp rides its own filter of the same stock kind rather than
	// sharing purple's. They are independently levelled controls, and one
	// filter driven from two tiles would have each of them silently
	// overwrite the other's settings every time it was touched.
	FXLavaFilterName = "reno-lava"
)

// FXPixelateBlocks is the width in pixels the pixelate chain downscales to
// at a level: 256 at level 1, a mosaic you have to look for, down to 16 at
// the top, where nothing survives. Geometric rather than linear, so every
// step is the same proportional bite -- the coarse end of the scale is
// where the visible difference per step lives, and a linear walk would
// spend most of its steps there and none at the fine end.
func FXPixelateBlocks(level int) int {
	const top, floor = 256.0, 16.0
	if level <= 1 {
		return int(top)
	}
	f := float64(level-1) / float64(FXMaxLevel-1)
	return max(int(math.Round(top*math.Pow(floor/top, f))), 1)
}

// FXPurpleTint is the color_multiply value at a level: white lerped
// toward violet, so level 1 is a wash and the top of the scale the whole
// drank.
//
// OBS packs a color setting as 0xAABBGGRR -- the LOW byte is RED, which is
// backwards from how the number reads. Verified against live OBS 32.2:
// color_multiply 0x0000FF on a webcam renders it red, not blue.
func FXPurpleTint(level int) int {
	const violetR, violetG, violetB = 170, 0, 255
	f := float64(level) / float64(FXMaxLevel)
	lerp := func(to int) int { return int(math.Round(255 + (float64(to)-255)*f)) }
	return lerp(violetB)<<16 | lerp(violetG)<<8 | lerp(violetR)
}

// CamFX is one effect invocation: the parsed form of a `cam ...` script
// command, a `khudson reno obs cam ...` argv, and a pad tile's act.
type CamFX struct {
	Kind string `json:"kind"`
	// Preset is the move/jump target placement.
	Preset string `json:"preset,omitempty"`
	// Dur and Ease shape a move's path.
	Dur  time.Duration `json:"dur,omitempty"`
	Ease string        `json:"ease,omitempty"`
	// Level is the magnitude of a level effect; 0 turns it off.
	Level int `json:"level,omitempty"`
}

// On reports whether a level effect is asking for anything at all.
func (f CamFX) On() bool { return f.Level > 0 }

// FXPlacements is the placement vocabulary an FX may target: the webcam
// presets minus "off", which is a visibility state (cam hide), not a
// place to move to.
func FXPlacements() []string {
	out := make([]string, 0, len(WebcamPresets))
	for _, p := range WebcamPresets {
		if p != "off" {
			out = append(out, p)
		}
	}
	return out
}

// CheckPlacement vets an FX target placement.
func CheckPlacement(p string) error {
	if !slices.Contains(FXPlacements(), p) {
		return fmt.Errorf("cam target %q wants one of %s", p, strings.Join(FXPlacements(), "|"))
	}
	return nil
}

// ParseCamFX parses the one FX argument syntax every surface speaks:
// `move <preset> [dur] [ease]`, `jump <preset>`, `show`, `hide`,
// `pixelate on|off [blocks]`.
func ParseCamFX(args []string) (CamFX, error) {
	if len(args) == 0 {
		return CamFX{}, fmt.Errorf("cam wants one of %s", strings.Join(CamFXKinds, "|"))
	}
	fx := CamFX{Kind: args[0]}
	rest := args[1:]
	switch fx.Kind {
	case FXShow, FXHide:
		if len(rest) != 0 {
			return CamFX{}, fmt.Errorf("cam %s takes no arguments", fx.Kind)
		}
	case FXJump:
		if len(rest) != 1 {
			return CamFX{}, errors.New("cam jump wants one placement preset")
		}
		fx.Preset = rest[0]
	case FXMove:
		if len(rest) < 1 || len(rest) > 3 {
			return CamFX{}, errors.New("cam move wants a placement preset, an optional duration, and an optional ease")
		}
		fx.Preset, fx.Dur, fx.Ease = rest[0], FXDefaultDur, FXDefaultEase
		for _, a := range rest[1:] {
			d, err := time.ParseDuration(a)
			if err != nil {
				// not a duration, so it is the ease; CheckCamFX rejects a
				// name that is neither
				fx.Ease = a
				continue
			}
			fx.Dur = d
		}
	case FXPixelate, FXPurple, FXJitter, FXLava:
		if len(rest) != 1 {
			return CamFX{}, fmt.Errorf("cam %s wants a level 0..%d (or on|off)", fx.Kind, FXMaxLevel)
		}
		if IsSustainFX(fx.Kind) {
			// the undriven span, so the batch budget and the sampler agree
			// without a second constant on the wire
			fx.Dur = FXSustainOneShot
		}
		switch rest[0] {
		case "on":
			fx.Level = FXDefaultLevel
		case "off":
			fx.Level = 0
		default:
			n, err := strconv.Atoi(rest[0])
			if err != nil {
				return CamFX{}, fmt.Errorf("cam %s level %q is not a number", fx.Kind, rest[0])
			}
			fx.Level = n
		}
	default:
		return CamFX{}, fmt.Errorf("cam %q wants one of %s", fx.Kind, strings.Join(CamFXKinds, "|"))
	}
	return fx, CheckCamFX(fx)
}

// CheckCamFX vets a parsed effect. Script vet, the CLI, and the plan
// builder all call it, so a bad effect dies at authoring time.
func CheckCamFX(fx CamFX) error {
	switch fx.Kind {
	case FXShow, FXHide:
	case FXJump, FXMove:
		if err := CheckPlacement(fx.Preset); err != nil {
			return err
		}
		if fx.Kind != FXMove {
			return nil
		}
		if fx.Dur <= 0 || fx.Dur > FXMaxDur {
			return fmt.Errorf("cam move duration %s wants to be positive and at most %s", fx.Dur, FXMaxDur)
		}
		if !slices.Contains(FXEases, fx.Ease) {
			return fmt.Errorf("cam move ease %q wants one of %s", fx.Ease, strings.Join(FXEases, "|"))
		}
	case FXPixelate, FXPurple, FXJitter, FXLava:
		if fx.Level < 0 || fx.Level > FXMaxLevel {
			return fmt.Errorf("cam %s level %d wants 0..%d", fx.Kind, fx.Level, FXMaxLevel)
		}
	default:
		return fmt.Errorf("cam %q wants one of %s", fx.Kind, strings.Join(CamFXKinds, "|"))
	}
	return nil
}

// Args is the inverse of ParseCamFX: the argv tail the module hands the
// verb. Round-tripping keeps the pad's taps and the CLI's flags on one
// parser instead of two that drift.
func (f CamFX) Args() []string {
	switch f.Kind {
	case FXMove:
		return []string{FXMove, f.Preset, f.Dur.String(), f.Ease}
	case FXJump:
		return []string{FXJump, f.Preset}
	case FXPixelate, FXPurple, FXJitter, FXLava:
		return []string{f.Kind, strconv.Itoa(f.Level)}
	}
	return []string{f.Kind}
}

// String is the one-line human form for journals and the pad readout.
func (f CamFX) String() string { return strings.Join(f.Args(), " ") }

// FXRequest is one obs-websocket request in a frame-locked batch: the
// wire shape, built pure so the whole path is unit-testable without OBS.
type FXRequest struct {
	Type string         `json:"requestType"`
	Data map[string]any `json:"requestData,omitempty"`
}

// FXEnv is the live OBS state an FX plan is built against: the canvas,
// the frame rate the sleeps are denominated in, the resolved cam item,
// and (for the paths that need them) the item's current pose and the
// filter capability probe.
type FXEnv struct {
	CanvasW, CanvasH float64
	FPS              float64
	Scene            string
	Source           string
	ItemID           int
	// From is the item's current transform; zero bounds mean the pose is
	// unknown and a move has nothing to interpolate out of.
	From Transform
	// SourceW/SourceH are the item's source dimensions, which pixelate has
	// to restore exactly: a bounds box derives its scale ONCE, so a filter
	// chain that hands back a different size than it was given leaves the
	// item rendering at a scale computed for the old one.
	SourceW, SourceH int
	// FilterKinds is what this OBS advertises (nil when the server has no
	// filter-kind list at all); Filters names the filters already on
	// Source.
	FilterKinds []string
	Filters     []string
}

// FXPlan is the desired effect as data: an ordered request sequence for
// one SERIAL_FRAME batch. An empty sequence is legal -- turning off an
// effect that was never on has nothing to send.
type FXPlan struct {
	Requests []FXRequest
	Summary  string
}

func (p *FXPlan) add(reqs ...FXRequest) { p.Requests = append(p.Requests, reqs...) }

// BuildCamFXPlan computes the request sequence for fx against env.
func BuildCamFXPlan(fx CamFX, env FXEnv) (*FXPlan, error) {
	if err := CheckCamFX(fx); err != nil {
		return nil, err
	}
	p := &FXPlan{Summary: fx.String()}
	switch fx.Kind {
	case FXShow:
		p.add(enableReq(env, true))
	case FXHide:
		p.add(enableReq(env, false))
	case FXJump:
		p.add(enableReq(env, true),
			transformReq(env, CamTransform(env.CanvasW, env.CanvasH, fx.Preset, env.SourceW, env.SourceH)))
	case FXMove:
		if err := addMove(p, fx, env); err != nil {
			return nil, err
		}
	case FXPixelate, FXPurple:
		if err := addLevel(p, fx, env); err != nil {
			return nil, err
		}
	case FXJitter, FXLava:
		if err := addSustainOneShot(p, fx, env); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// A sustained effect is an oscillation nobody ever asked to stop at a
// particular point: a shake, a colour drift. Three plans express one, and
// the driver (internal/bus SustainFX) runs them in that order:
//
//	SustainSetup   once, before anything moves: whatever the effect needs
//	               to exist -- the item enabled, the filter created.
//	SustainChunk   over and over, each carrying the PHASE the last one
//	               ended at, so the oscillation is continuous across a
//	               boundary the viewer must never be able to see.
//	SustainHome    once, on the way out: the state the effect found.
//
// The phase is the whole trick. The old shake re-fired batches of WHOLE
// cycles, because each batch re-read the camera's pose and had to hand the
// next one the pose it started from -- which forced a chunk long enough to
// hold whole cycles, which was the 3s a tap on "off" had to wait out. A
// driver that holds its own phase reads the pose ONCE, so a chunk can be as
// short as the off latency wants it.

// sustainAmp is an effect's magnitude at a level, as a fraction 0..1 of
// its own top.
func sustainAmp(level int) float64 { return float64(level) / float64(FXMaxLevel) }

// sustainHz is an effect's frequency in whole oscillations per second.
func sustainHz(kind string) float64 {
	switch kind {
	case FXJitter:
		return FXJitterHz
	case FXLava:
		return FXLavaHz
	}
	return 0
}

// sustainSampler is an effect reduced to one function: the request that
// renders it at a given phase, measured in whole turns. Everything that
// differs between a shake and a colour drift lives in here; the chunk
// arithmetic above it does not know which one it is driving.
func sustainSampler(fx CamFX, env FXEnv) (func(turns float64) FXRequest, error) {
	switch fx.Kind {
	case FXJitter:
		if env.From.BoundsW <= 0 || env.From.BoundsH <= 0 {
			return nil, errors.New("reno-cam has no bounds box to shake -- set a webcam preset first (khudson reno config --webcam br)")
		}
		amp := FXJitterMaxFrac * sustainAmp(fx.Level) * env.From.BoundsW
		// Fit the throw to the room the box actually has, per axis, rather
		// than clamping each pose as it is computed. Clamping flattens the
		// half of the swing that runs out of canvas, and a shake with one
		// side squared off does not read as a shake -- it reads as the
		// camera being shoved into the corner and sliding back (glass: a br
		// PiP at the top level has 24px of room to its right and swings 77px
		// left, so it just lurches). A smaller symmetric shake is the honest
		// rendering of a box with nowhere to go.
		ampX := min(amp, headroom(env.From.X, env.From.BoundsW, env.CanvasW))
		ampY := min(0.6*amp, headroom(env.From.Y, env.From.BoundsH, env.CanvasH))
		return func(turns float64) FXRequest {
			ph := 2 * math.Pi * turns
			at := env.From
			at.X = clampAxis(env.From.X+ampX*math.Sin(ph), env.CanvasW, env.From.BoundsW)
			// the vertical throw is smaller and at double the rate, so the
			// shake traces a figure eight instead of sliding along one diagonal
			at.Y = clampAxis(env.From.Y+ampY*math.Sin(2*ph), env.CanvasH, env.From.BoundsH)
			return transformReq(env, at)
		}, nil
	case FXLava:
		return func(turns float64) FXRequest {
			return FXRequest{Type: "SetSourceFilterSettings", Data: map[string]any{
				"sourceName": env.Source, "filterName": FXLavaFilterName,
				"filterSettings": lavaSettings(fx.Level, turns), "overlay": true,
			}}
		}, nil
	}
	return nil, fmt.Errorf("cam %s is not a sustained effect", fx.Kind)
}

// lavaSettings is the lamp at one instant: the hue swung either side of the
// camera's own colour, with the saturation held up so the swing reads as
// light rather than as a wash. Hue only oscillates -- it does not run all
// the way round -- because a full rotation spends half its time with skin
// the colour of a bruise, and stopping the effect from a hue on the far
// side would snap rather than settle.
func lavaSettings(level int, turns float64) map[string]any {
	amp := sustainAmp(level)
	return map[string]any{
		"hue_shift":  FXLavaMaxShift * amp * math.Sin(2*math.Pi*turns),
		"saturation": 1 + (FXLavaSat-1)*amp,
	}
}

// SustainSetup is what an effect needs in place before it moves: the cam
// item visible either way, plus the filter a colour effect drives. Filter
// creation goes here rather than into the chunk because a chunk runs four
// times a second and creating a filter is not something to re-decide at
// that rate.
func SustainSetup(fx CamFX, env FXEnv) (*FXPlan, error) {
	if err := CheckCamFX(fx); err != nil {
		return nil, err
	}
	if !IsSustainFX(fx.Kind) {
		return nil, fmt.Errorf("cam %s is not a sustained effect", fx.Kind)
	}
	p := &FXPlan{Summary: fx.String() + " (setup)"}
	p.add(enableReq(env, true))
	if fx.Kind == FXLava {
		if err := addLevel(p, fx, env); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// SustainChunk is one batch of a running effect: dur of oscillation
// starting at phase, and the phase the next chunk picks up from. The phase
// is returned rather than recomputed because that is what makes the seam
// between two chunks invisible.
func SustainChunk(fx CamFX, env FXEnv, phase float64, dur time.Duration) (*FXPlan, float64, error) {
	at, err := sustainSampler(fx, env)
	if err != nil {
		return nil, phase, err
	}
	fps := env.FPS
	if fps <= 0 {
		fps = FXFallbackFPS
	}
	frames := max(int(math.Round(dur.Seconds()*fps)), 2)
	samples := min(frames, FXSustainSamples)
	sleep := max(frames/samples, 1)
	turns := sustainHz(fx.Kind) * dur.Seconds()
	p := &FXPlan{Summary: fx.String()}
	for i := 1; i <= samples; i++ {
		if i > 1 {
			p.add(sleepReq(sleep))
		}
		p.add(at(phase + turns*float64(i)/float64(samples)))
	}
	return p, phase + turns, nil
}

// SustainHome is the state the effect found, restored: the pose a shake
// started from, the filter a lamp switched on. It is the only thing that
// puts either back -- a chunk deliberately ends wherever the phase left it
// -- so the driver owes this plan on every exit path it has.
//
// The pose comes from env.From, read ONCE before the first chunk. Reading
// it now would capture the camera mid-swing and enshrine that as home.
func SustainHome(fx CamFX, env FXEnv) (*FXPlan, error) {
	p := &FXPlan{Summary: fx.String() + " (home)"}
	switch fx.Kind {
	case FXJitter:
		if env.From.BoundsW <= 0 || env.From.BoundsH <= 0 {
			return nil, errors.New("no base pose to put the camera back to")
		}
		p.add(transformReq(env, env.From))
	case FXLava:
		p.add(filterEnabledReq(env, FXLavaFilterName, false))
	default:
		return nil, fmt.Errorf("cam %s is not a sustained effect", fx.Kind)
	}
	return p, nil
}

// addSustainOneShot is a sustained effect with nobody driving it: setup,
// one visible span of oscillation, and home. This is what a bare CLI call
// or a script step gets, and it ends where it started because there is no
// driver left afterwards to put anything back.
func addSustainOneShot(p *FXPlan, fx CamFX, env FXEnv) error {
	if !fx.On() {
		// the driver restores home on its way out and a one-shot already
		// ended there, so there is nothing here to undo. Lava is the
		// exception: its filter is state, and switching it off is real work
		if fx.Kind == FXLava {
			return addLevel(p, fx, env)
		}
		p.Summary += " (stopped)"
		return nil
	}
	setup, err := SustainSetup(fx, env)
	if err != nil {
		return err
	}
	dur := fx.Dur
	if dur <= 0 {
		dur = FXSustainOneShot
	}
	span, _, err := SustainChunk(fx, env, 0, dur)
	if err != nil {
		return err
	}
	home, err := SustainHome(fx, env)
	if err != nil {
		return err
	}
	p.add(setup.Requests...)
	p.add(span.Requests...)
	p.add(home.Requests...)
	return nil
}

// clampAxis keeps a shaken box inside the canvas on one axis. A canvas
// smaller than the box (a "big" preset against a tiny canvas) has nowhere
// to clamp to, so it pins at the origin rather than going negative. With a
// headroom-fitted amplitude this is a backstop rather than the mechanism.
func clampAxis(v, canvas, box float64) float64 {
	return min(max(v, 0), max(canvas-box, 0))
}

// headroom is how far a box at pos can travel on one axis and still land
// back inside the canvas travelling the SAME distance the other way: the
// smaller of the two margins, since a symmetric swing is bounded by the
// tighter side.
func headroom(pos, box, canvas float64) float64 {
	return max(min(pos, canvas-(pos+box)), 0)
}

// addMove samples the eased path from the item's current pose to the
// target placement. A move implies visible: a hidden cam sliding in is
// the whole point, so the enable leads the path.
func addMove(p *FXPlan, fx CamFX, env FXEnv) error {
	if env.From.BoundsW <= 0 || env.From.BoundsH <= 0 {
		return errors.New("reno-cam has no bounds box to move from -- set a webcam preset first (khudson reno config --webcam br)")
	}
	fps := env.FPS
	if fps <= 0 {
		fps = FXFallbackFPS
	}
	frames := max(int(math.Round(fx.Dur.Seconds()*fps)), 2)
	samples := min(frames, FXMaxSamples)
	sleep := max(frames/samples, 1)
	to := CamTransform(env.CanvasW, env.CanvasH, fx.Preset, env.SourceW, env.SourceH)
	p.add(enableReq(env, true))
	for i := 1; i <= samples; i++ {
		if i > 1 {
			p.add(sleepReq(sleep))
		}
		p.add(transformReq(env, lerpTransform(env.From, to, Ease(fx.Ease, float64(i)/float64(samples)))))
	}
	return nil
}

// levelStage is one khudson-owned filter in a level effect's chain: the
// name it occupies on reno-cam, the stock kind it needs OBS to advertise,
// and its settings at this level. Purple is one stage, pixelate is two.
type levelStage struct {
	Name     string
	Kind     string
	Settings map[string]any
}

// levelStages is the filter chain a level effect wants, in the order OBS
// must run it (index 0 first).
func levelStages(fx CamFX, env FXEnv) []levelStage {
	switch fx.Kind {
	case FXPixelate:
		return pixelateStages(fx, env)
	case FXPurple:
		// Multiply and not hue_shift: a hue rotation moves every colour in
		// the frame and the result depends on what was in it, where a tint
		// is the same wash on any source, which is what a level is supposed
		// to promise. The lamp below is the effect that WANTS the dependence.
		return []levelStage{{
			Name: FXPurpleFilterName, Kind: FXPurpleFilterKind,
			Settings: map[string]any{"color_multiply": FXPurpleTint(fx.Level)},
		}}
	case FXLava:
		// phase zero: the lamp is created sitting on the camera's own hue,
		// so switching it on is not itself a visible jump. Every instant
		// after this one comes from the driver.
		return []levelStage{{
			Name: FXLavaFilterName, Kind: FXPurpleFilterKind,
			Settings: lavaSettings(fx.Level, 0),
		}}
	}
	return nil
}

// levelStageNames is the chain's names alone, for the disable path -- which
// needs them without a level to compute settings from.
func levelStageNames(kind string) []string {
	switch kind {
	case FXPixelate:
		return []string{FXPixelateDownName, FXPixelateUpName}
	case FXPurple:
		return []string{FXPurpleFilterName}
	case FXLava:
		return []string{FXLavaFilterName}
	}
	return nil
}

// addLevel drives a level effect's filter chain. The capability probe is
// the only guard between the effect and a request OBS does not understand,
// so an unadvertised kind refuses by name instead of failing mid-batch.
//
// Level 0 disables the stages that are there and refuses to invent the
// ones that are not: turning off an effect that was never on has nothing
// to send. It leaves the settings alone, so the next level re-enables into
// a chain that is already shaped right.
func addLevel(p *FXPlan, fx CamFX, env FXEnv) error {
	if !fx.On() {
		var off int
		for _, name := range levelStageNames(fx.Kind) {
			if slices.Contains(env.Filters, name) {
				p.add(filterEnabledReq(env, name, false))
				off++
			}
		}
		if off == 0 {
			p.Summary += " (no filter to disable)"
		}
		return nil
	}
	if fx.Kind == FXPixelate && (env.SourceW <= 0 || env.SourceH <= 0) {
		// the chain has to hand the item back the size it was given, and
		// there is no honest one to restore
		return errors.New("cam pixelate: reno-cam reports no source size, so the grid it would scale back up to is unknown")
	}
	stages := levelStages(fx, env)
	if len(stages) == 0 {
		return fmt.Errorf("cam %s: no level filter", fx.Kind)
	}
	for _, s := range stages {
		if len(env.FilterKinds) == 0 {
			return fmt.Errorf("cam %s: this obs advertises no filter kinds, so %q cannot be confirmed", fx.Kind, s.Kind)
		}
		if !slices.Contains(env.FilterKinds, s.Kind) {
			return fmt.Errorf("cam %s: this obs has no %q filter kind (%d kinds advertised)", fx.Kind, s.Kind, len(env.FilterKinds))
		}
	}
	// Created in chain order, because CreateSourceFilter appends: a chain
	// whose stages arrived out of order gets pinned back below.
	for _, s := range stages {
		if slices.Contains(env.Filters, s.Name) {
			p.add(FXRequest{Type: "SetSourceFilterSettings", Data: map[string]any{
				"sourceName": env.Source, "filterName": s.Name,
				"filterSettings": s.Settings, "overlay": true,
			}})
		} else {
			p.add(FXRequest{Type: "CreateSourceFilter", Data: map[string]any{
				"sourceName": env.Source, "filterName": s.Name,
				"filterKind": s.Kind, "filterSettings": s.Settings,
			}})
		}
	}
	addStageOrder(p, stages, env)
	for _, s := range stages {
		p.add(filterEnabledReq(env, s.Name, true))
	}
	return nil
}

// addStageOrder repairs a chain whose stages sit in the wrong order on the
// source. It matters for pixelate and nothing else: the up stage exists to
// force the intermediate texture the down stage produced, so upstream of
// the downscale it does nothing at all.
func addStageOrder(p *FXPlan, stages []levelStage, env FXEnv) {
	if len(stages) < 2 {
		return
	}
	want := make([]string, 0, len(stages))
	for _, s := range stages {
		want = append(want, s.Name)
	}
	// the order the chain WILL have once the creates above land: they
	// append, so a stage that was missing arrives at the tail
	after := slices.Clone(env.Filters)
	for _, name := range want {
		if !slices.Contains(after, name) {
			after = append(after, name)
		}
	}
	if slices.Equal(filterOrder(after, want), want) {
		return
	}
	base := len(after) - len(want) // stages sort to the chain's tail
	for i, name := range want {
		p.add(FXRequest{Type: "SetSourceFilterIndex", Data: map[string]any{
			"sourceName": env.Source, "filterName": name, "filterIndex": base + i,
		}})
	}
}

// filterOrder is order's entries that appear in keep, order preserved.
func filterOrder(order, keep []string) []string {
	out := make([]string, 0, len(keep))
	for _, name := range order {
		if slices.Contains(keep, name) {
			out = append(out, name)
		}
	}
	return out
}

// pixelateStages is the pixelate chain: downscale to the level's grid,
// then straight back up to the source's own size, both point-sampled.
//
// Neither stage is decoration. A LONE downscale is a visual no-op (glass,
// 2026-08-22): it reports the small size, so the item's transform math sees
// 16x9, but obs-filters renders the scale filter with
// OBS_ALLOW_DIRECT_RENDERING, so the parent goes straight to the
// destination rect at full resolution and the frame comes out sharp. The
// second stage forces the intermediate texture, and point-sampling out of
// it is what turns it into blocks.
//
// The upscale goes back to the size the chain was HANDED, which is the only
// size that leaves the item's scale alone. Sizing it to anything else --
// the item's own box, say, which makes the scale come out at a tidy 1 --
// breaks the moment the level drops to 0: the filters switch off, the
// source reverts to its native size, and a bounds box that derived its
// scale once keeps the one it computed for the other size. The camera then
// renders at 1920x1080 inside a 768x432 PiP, spilling off the canvas, and
// nothing about the effect being OFF suggests that is what happened.
func pixelateStages(fx CamFX, env FXEnv) []levelStage {
	w, h := env.SourceW, env.SourceH
	blocks := FXPixelateBlocks(fx.Level)
	rows := max(int(math.Round(float64(blocks)*float64(h)/float64(w))), 1)
	return []levelStage{
		{
			Name: FXPixelateDownName, Kind: FXFilterKind,
			Settings: map[string]any{
				"resolution": fmt.Sprintf("%dx%d", blocks, rows),
				"sampling":   "point",
			},
		},
		{
			Name: FXPixelateUpName, Kind: FXFilterKind,
			Settings: map[string]any{
				"resolution": fmt.Sprintf("%dx%d", w, h),
				"sampling":   "point",
			},
		},
	}
}

func enableReq(env FXEnv, on bool) FXRequest {
	return FXRequest{Type: "SetSceneItemEnabled", Data: map[string]any{
		"sceneName": env.Scene, "sceneItemId": env.ItemID, "sceneItemEnabled": on,
	}}
}

func transformReq(env FXEnv, t Transform) FXRequest {
	return FXRequest{Type: "SetSceneItemTransform", Data: map[string]any{
		"sceneName": env.Scene, "sceneItemId": env.ItemID,
		"sceneItemTransform": TransformFields(t),
	}}
}

// sleepReq paces the next request n rendered frames later; legal only
// inside a SERIAL_FRAME or SERIAL_REALTIME batch.
func sleepReq(frames int) FXRequest {
	return FXRequest{Type: "Sleep", Data: map[string]any{"sleepFrames": frames}}
}

func filterEnabledReq(env FXEnv, filter string, on bool) FXRequest {
	return FXRequest{Type: "SetSourceFilterEnabled", Data: map[string]any{
		"sourceName": env.Source, "filterName": filter, "filterEnabled": on,
	}}
}

// TransformFields is the obs-websocket sceneItemTransform object for t:
// the same top-left-anchored scale-inner box the single-shot transport
// call sends, as data so a batch can carry it.
func TransformFields(t Transform) map[string]any {
	return map[string]any{
		"positionX": t.X, "positionY": t.Y,
		"alignment":       5,
		"boundsType":      "OBS_BOUNDS_SCALE_INNER",
		"boundsAlignment": 0,
		"boundsWidth":     t.BoundsW, "boundsHeight": t.BoundsH,
		"cropLeft": t.CropL, "cropTop": t.CropT,
		"cropRight": t.CropR, "cropBottom": t.CropB,
	}
}

// lerpTransform interpolates the box; crops come from the destination
// (a placement never crops, and a half-applied crop would tear).
func lerpTransform(from, to Transform, t float64) Transform {
	lerp := func(a, b float64) float64 { return a + (b-a)*t }
	return Transform{
		X: lerp(from.X, to.X), Y: lerp(from.Y, to.Y),
		BoundsW: lerp(from.BoundsW, to.BoundsW), BoundsH: lerp(from.BoundsH, to.BoundsH),
		CropL: to.CropL, CropT: to.CropT, CropR: to.CropR, CropB: to.CropB,
	}
}

// Ease maps linear progress onto the named curve; an unknown name is
// linear (CheckCamFX is what rejects one).
func Ease(name string, t float64) float64 {
	t = min(max(t, 0), 1)
	switch name {
	case "smooth":
		return t * t * (3 - 2*t)
	case "bounce":
		return easeOutBounce(t)
	}
	return t
}

// easeOutBounce is the standard four-segment bounce -- the cam lands and
// settles. It stays inside [0,1], so no sample ever leaves the canvas.
func easeOutBounce(t float64) float64 {
	const n, d = 7.5625, 2.75
	switch {
	case t < 1/d:
		return n * t * t
	case t < 2/d:
		t -= 1.5 / d
		return n*t*t + 0.75
	case t < 2.5/d:
		t -= 2.25 / d
		return n*t*t + 0.9375
	}
	t -= 2.625 / d
	return n*t*t + 0.984375
}
