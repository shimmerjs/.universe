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
)

// CamFXKinds is the vocabulary in error-message order.
var CamFXKinds = []string{FXMove, FXJump, FXShow, FXHide, FXPixelate}

// FXEases names the interpolation curves a move may take.
var FXEases = []string{"linear", "smooth", "bounce"}

// FX defaults and bounds. The duration cap keeps a batch well inside the
// verb watchdog; the sample cap keeps the request count sane on a long
// path -- past it the samples stride over more frames each instead of
// multiplying. Blocks is the pixelate grid's width in pixels.
const (
	FXDefaultDur    = 500 * time.Millisecond
	FXDefaultEase   = "smooth"
	FXDefaultBlocks = 48
	FXMaxDur        = 10 * time.Second
	FXMaxSamples    = 90
	FXMinBlocks     = 8
	FXMaxBlocks     = 480
	// FXFallbackFPS stands in when OBS reports no frame rate: every
	// sleep is denominated in frames, so a wrong divisor only mis-paces
	// a move, it never breaks one.
	FXFallbackFPS = 60.0
)

// FXFilterKind is the stock filter the pixelate FX drives: OBS's own
// Scaling/Aspect Ratio filter, downscaling the camera to a handful of
// blocks with point sampling. The design's composite-blur path is NOT
// built -- it is a hand-installed plugin under a self-updating cask, and
// a settings schema nobody here can verify is worse than an honest
// refusal -- so pixelate fails loudly when even this kind is absent.
const FXFilterKind = "scale_filter"

// FXFilterName is the khudson-owned filter on the reno-cam input.
const FXFilterName = "reno-pixelate"

// CamFX is one effect invocation: the parsed form of a `cam ...` script
// command, a `khudson reno obs cam ...` argv, and a pad tile's act.
type CamFX struct {
	Kind string `json:"kind"`
	// Preset is the move/jump target placement.
	Preset string `json:"preset,omitempty"`
	// Dur and Ease shape a move's path.
	Dur  time.Duration `json:"dur,omitempty"`
	Ease string        `json:"ease,omitempty"`
	// On and Blocks are the pixelate knobs.
	On     bool `json:"on,omitempty"`
	Blocks int  `json:"blocks,omitempty"`
}

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
	case FXPixelate:
		if len(rest) < 1 || len(rest) > 2 {
			return CamFX{}, errors.New("cam pixelate wants on|off and an optional block width")
		}
		switch rest[0] {
		case "on":
			fx.On = true
		case "off":
		default:
			return CamFX{}, fmt.Errorf("cam pixelate wants on or off, got %q", rest[0])
		}
		fx.Blocks = FXDefaultBlocks
		if len(rest) == 2 {
			n, err := strconv.Atoi(rest[1])
			if err != nil {
				return CamFX{}, fmt.Errorf("cam pixelate block width %q is not a number", rest[1])
			}
			fx.Blocks = n
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
	case FXPixelate:
		if fx.Blocks < FXMinBlocks || fx.Blocks > FXMaxBlocks {
			return fmt.Errorf("cam pixelate block width %d wants %d..%d", fx.Blocks, FXMinBlocks, FXMaxBlocks)
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
	case FXPixelate:
		state := "off"
		if f.On {
			state = "on"
		}
		return []string{FXPixelate, state, strconv.Itoa(f.Blocks)}
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
			transformReq(env, CamTransform(env.CanvasW, env.CanvasH, fx.Preset)))
	case FXMove:
		if err := addMove(p, fx, env); err != nil {
			return nil, err
		}
	case FXPixelate:
		if err := addPixelate(p, fx, env); err != nil {
			return nil, err
		}
	}
	return p, nil
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
	to := CamTransform(env.CanvasW, env.CanvasH, fx.Preset)
	p.add(enableReq(env, true))
	for i := 1; i <= samples; i++ {
		if i > 1 {
			p.add(sleepReq(sleep))
		}
		p.add(transformReq(env, lerpTransform(env.From, to, Ease(fx.Ease, float64(i)/float64(samples)))))
	}
	return nil
}

// addPixelate drives the stock scaling filter. The capability probe is
// the only guard between "pixelate" and a request OBS does not
// understand, so an unadvertised kind refuses by name instead of failing
// mid-batch.
func addPixelate(p *FXPlan, fx CamFX, env FXEnv) error {
	if !fx.On {
		if !slices.Contains(env.Filters, FXFilterName) {
			p.Summary += " (no filter to disable)"
			return nil
		}
		p.add(filterEnabledReq(env, false))
		return nil
	}
	if len(env.FilterKinds) == 0 {
		return fmt.Errorf("cam pixelate: this obs advertises no filter kinds, so %q cannot be confirmed", FXFilterKind)
	}
	if !slices.Contains(env.FilterKinds, FXFilterKind) {
		return fmt.Errorf("cam pixelate: this obs has no %q filter kind (%d kinds advertised)", FXFilterKind, len(env.FilterKinds))
	}
	// the grid follows the item's aspect when the pose is known, the
	// canvas otherwise -- the camera's own source dimensions never reach
	// this plan
	aspectW, aspectH := env.CanvasW, env.CanvasH
	if env.From.BoundsW > 0 && env.From.BoundsH > 0 {
		aspectW, aspectH = env.From.BoundsW, env.From.BoundsH
	}
	h := 1
	if aspectW > 0 {
		h = max(int(math.Round(float64(fx.Blocks)*aspectH/aspectW)), 1)
	}
	settings := map[string]any{
		"resolution": fmt.Sprintf("%dx%d", fx.Blocks, h),
		"sampling":   "point",
	}
	if slices.Contains(env.Filters, FXFilterName) {
		p.add(FXRequest{Type: "SetSourceFilterSettings", Data: map[string]any{
			"sourceName": env.Source, "filterName": FXFilterName,
			"filterSettings": settings, "overlay": true,
		}})
	} else {
		p.add(FXRequest{Type: "CreateSourceFilter", Data: map[string]any{
			"sourceName": env.Source, "filterName": FXFilterName,
			"filterKind": FXFilterKind, "filterSettings": settings,
		}})
	}
	p.add(filterEnabledReq(env, true))
	return nil
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

func filterEnabledReq(env FXEnv, on bool) FXRequest {
	return FXRequest{Type: "SetSourceFilterEnabled", Data: map[string]any{
		"sourceName": env.Source, "filterName": FXFilterName, "filterEnabled": on,
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
