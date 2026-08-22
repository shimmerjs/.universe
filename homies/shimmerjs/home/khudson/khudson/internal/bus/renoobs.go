// OBS transport for the reno record verbs: one short-lived goobs
// connection per operation (localhost websocket; connect cost is noise
// next to a recording). Verbs own all OBS I/O -- the module journals
// intent and outcomes but never dials, so a wedged OBS can never stall
// the bus. Capability-probed at connect: requests outside the server's
// advertised set degrade explicitly instead of erroring obscurely
// (SetRecordDirectory is the one optional dependency -- without it the
// clip lands in OBS's default directory and the journaled output path
// still owns the association).
package bus

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/andreykaipov/goobs"
	"github.com/andreykaipov/goobs/api/requests/config"
	"github.com/andreykaipov/goobs/api/requests/filters"
	"github.com/andreykaipov/goobs/api/requests/inputs"
	"github.com/andreykaipov/goobs/api/requests/sceneitems"
	"github.com/andreykaipov/goobs/api/requests/scenes"
	"github.com/andreykaipov/goobs/api/requests/ui"
	"github.com/andreykaipov/goobs/api/typedefs"

	"github.com/shimmerjs/khudson/khudson/internal/paths"
	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// ObsTransport is the operation seam; goobsTransport is the real one,
// tests fake it.
type ObsTransport interface {
	// Version reports the connected OBS version (identity for the run
	// journal).
	Version() string
	// CanSetRecordDirectory reports the capability probe's verdict.
	CanSetRecordDirectory() bool
	SetRecordDirectory(dir string) error
	SetScene(name string) error
	StartRecord() error
	// StopRecord returns the finished clip's output path.
	StopRecord() (string, error)
	PauseRecord() error
	ResumeRecord() error
	Close() error

	// Scene-plan surface (the ad hoc expansion): enumeration plus the
	// minimal mutations BuildScenePlan's executor needs.
	InputKinds() ([]string, error)
	Monitors() ([]renocore.Monitor, error)
	// Canvas is the base resolution plus the frame rate FX sleeps are
	// denominated in.
	Canvas() (w, h, fps float64, err error)
	Scenes() ([]string, error)
	CreateScene(name string) error
	// Inputs maps input name -> kind for every OBS input.
	Inputs() (map[string]string, error)
	// CreateInput creates an input as a scene item; settings may be nil.
	CreateInput(scene, name, kind string, settings map[string]any, enabled bool) error
	SetInputSettings(name string, settings map[string]any) error
	SetInputMute(name string, muted bool) error
	// VirtualCamActive reports whether OBS's virtual camera output is
	// running. It is an output nothing here starts, but macOS starts it on
	// demand when any app opens the OBS Virtual Camera device -- so it can
	// hold the camera indefinitely while the pad reports everything idle.
	VirtualCamActive() (bool, error)
	StopVirtualCam() error
	RemoveInput(name string) error
	// PropertyItems enumerates a list-property's items (displays,
	// devices), values stringified.
	PropertyItems(input, prop string) ([]renocore.PropertyItem, error)
	// SceneItemID resolves source's item in scene; ensure=true adds the
	// existing input to the scene when it is not yet an item there.
	SceneItemID(scene, source string, ensure bool) (int, error)
	SetSceneItemEnabled(scene string, id int, enabled bool) error
	// ItemSourceSize reads the item's source dimensions (0,0 while the
	// source is not yet streaming, e.g. pre-TCC).
	ItemSourceSize(scene string, id int) (w, h int, err error)
	SetSceneItemTransform(scene string, id int, t renocore.Transform) error

	// Webcam FX surface. ItemTransform reads the item's current pose (an
	// eased move interpolates out of it); FilterKinds is the capability
	// probe standing in front of every filter effect -- nil when the
	// server has no filter-kind list at all; SourceFilters names what is
	// already attached, so the plan creates or updates rather than
	// erroring on a duplicate.
	ItemTransform(scene string, id int) (renocore.Transform, error)
	FilterKinds() ([]string, error)
	SourceFilters(source string) ([]string, error)
	// RunFrameBatch executes reqs as one SERIAL_FRAME batch: frame-locked
	// in OBS, so Sleep pseudo-requests pace an animation. budget bounds
	// the exchange, playback time included.
	RunFrameBatch(id string, reqs []renocore.FXRequest, budget time.Duration) error
}

// DefaultObsHost is the obs-websocket default on this machine.
const DefaultObsHost = "localhost:4455"

// ObsPassword reads the optional websocket password from the state root
// (<state>/reno/obs-password, user-managed 0600); absent means authless
// localhost.
func ObsPassword() string {
	p, err := paths.Resolve()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(p.Dir, "reno", "obs-password"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

type goobsTransport struct {
	c       *goobs.Client
	version string
	reqs    []string
	// host/password are kept for the batch path, which cannot ride the
	// goobs connection (see obsbatch.go).
	host     string
	password string
}

// DialOBS connects and probes: version + the advertised request set.
func DialOBS(host, password string) (ObsTransport, error) {
	var opts []goobs.Option
	if password != "" {
		opts = append(opts, goobs.WithPassword(password))
	}
	c, err := goobs.New(host, opts...)
	if err != nil {
		return nil, fmt.Errorf("obs at %s: %w", host, err)
	}
	v, err := c.General.GetVersion()
	if err != nil {
		c.Disconnect()
		return nil, fmt.Errorf("obs version probe: %w", err)
	}
	return &goobsTransport{
		c: c, version: v.ObsVersion, reqs: v.AvailableRequests,
		host: host, password: password,
	}, nil
}

func (t *goobsTransport) Version() string { return t.version }

func (t *goobsTransport) CanSetRecordDirectory() bool {
	return slices.Contains(t.reqs, "SetRecordDirectory")
}

func (t *goobsTransport) SetRecordDirectory(dir string) error {
	_, err := t.c.Config.SetRecordDirectory(
		config.NewSetRecordDirectoryParams().WithRecordDirectory(dir))
	return err
}

func (t *goobsTransport) SetScene(name string) error {
	_, err := t.c.Scenes.SetCurrentProgramScene(
		scenes.NewSetCurrentProgramSceneParams().WithSceneName(name))
	return err
}

func (t *goobsTransport) StartRecord() error {
	_, err := t.c.Record.StartRecord()
	return err
}

func (t *goobsTransport) StopRecord() (string, error) {
	resp, err := t.c.Record.StopRecord()
	if err != nil {
		return "", err
	}
	return resp.OutputPath, nil
}

func (t *goobsTransport) PauseRecord() error {
	_, err := t.c.Record.PauseRecord()
	return err
}

func (t *goobsTransport) ResumeRecord() error {
	_, err := t.c.Record.ResumeRecord()
	return err
}

func (t *goobsTransport) Close() error { return t.c.Disconnect() }

func (t *goobsTransport) InputKinds() ([]string, error) {
	resp, err := t.c.Inputs.GetInputKindList(inputs.NewGetInputKindListParams())
	if err != nil {
		return nil, err
	}
	return resp.InputKinds, nil
}

func (t *goobsTransport) Monitors() ([]renocore.Monitor, error) {
	resp, err := t.c.Ui.GetMonitorList(&ui.GetMonitorListParams{})
	if err != nil {
		return nil, err
	}
	out := make([]renocore.Monitor, 0, len(resp.Monitors))
	for _, m := range resp.Monitors {
		out = append(out, renocore.Monitor{
			Index: m.MonitorIndex, Name: m.MonitorName,
			Width: m.MonitorWidth, Height: m.MonitorHeight,
		})
	}
	return out, nil
}

func (t *goobsTransport) Canvas() (float64, float64, float64, error) {
	resp, err := t.c.Config.GetVideoSettings()
	if err != nil {
		return 0, 0, 0, err
	}
	fps := 0.0
	if resp.FpsDenominator > 0 {
		fps = resp.FpsNumerator / resp.FpsDenominator
	}
	return resp.BaseWidth, resp.BaseHeight, fps, nil
}

func (t *goobsTransport) Scenes() ([]string, error) {
	resp, err := t.c.Scenes.GetSceneList()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp.Scenes))
	for _, s := range resp.Scenes {
		out = append(out, s.SceneName)
	}
	return out, nil
}

func (t *goobsTransport) CreateScene(name string) error {
	_, err := t.c.Scenes.CreateScene(scenes.NewCreateSceneParams().WithSceneName(name))
	return err
}

func (t *goobsTransport) Inputs() (map[string]string, error) {
	resp, err := t.c.Inputs.GetInputList(inputs.NewGetInputListParams())
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(resp.Inputs))
	for _, in := range resp.Inputs {
		out[in.InputName] = in.InputKind
	}
	return out, nil
}

func (t *goobsTransport) CreateInput(scene, name, kind string, settings map[string]any, enabled bool) error {
	p := inputs.NewCreateInputParams().
		WithSceneName(scene).WithInputName(name).WithInputKind(kind).
		WithSceneItemEnabled(enabled)
	if settings != nil {
		p = p.WithInputSettings(settings)
	}
	_, err := t.c.Inputs.CreateInput(p)
	return err
}

func (t *goobsTransport) SetInputSettings(name string, settings map[string]any) error {
	_, err := t.c.Inputs.SetInputSettings(inputs.NewSetInputSettingsParams().
		WithInputName(name).WithInputSettings(settings).WithOverlay(true))
	return err
}

func (t *goobsTransport) SetInputMute(name string, muted bool) error {
	_, err := t.c.Inputs.SetInputMute(inputs.NewSetInputMuteParams().
		WithInputName(name).WithInputMuted(muted))
	return err
}

func (t *goobsTransport) RemoveInput(name string) error {
	_, err := t.c.Inputs.RemoveInput(inputs.NewRemoveInputParams().WithInputName(name))
	return err
}

func (t *goobsTransport) PropertyItems(input, prop string) ([]renocore.PropertyItem, error) {
	resp, err := t.c.Inputs.GetInputPropertiesListPropertyItems(
		inputs.NewGetInputPropertiesListPropertyItemsParams().
			WithInputName(input).WithPropertyName(prop))
	if err != nil {
		return nil, err
	}
	out := make([]renocore.PropertyItem, 0, len(resp.PropertyItems))
	for _, it := range resp.PropertyItems {
		out = append(out, renocore.PropertyItem{
			Name:  it.ItemName,
			Value: fmt.Sprint(it.ItemValue),
		})
	}
	return out, nil
}

func (t *goobsTransport) SceneItemID(scene, source string, ensure bool) (int, error) {
	resp, err := t.c.SceneItems.GetSceneItemId(sceneitems.NewGetSceneItemIdParams().
		WithSceneName(scene).WithSourceName(source))
	if err == nil {
		return int(resp.SceneItemId), nil
	}
	if !ensure {
		return 0, err
	}
	created, cerr := t.c.SceneItems.CreateSceneItem(sceneitems.NewCreateSceneItemParams().
		WithSceneName(scene).WithSourceName(source).WithSceneItemEnabled(true))
	if cerr != nil {
		return 0, fmt.Errorf("item %s in %s: %w (create: %v)", source, scene, err, cerr)
	}
	return int(created.SceneItemId), nil
}

func (t *goobsTransport) SetSceneItemEnabled(scene string, id int, enabled bool) error {
	_, err := t.c.SceneItems.SetSceneItemEnabled(sceneitems.NewSetSceneItemEnabledParams().
		WithSceneName(scene).WithSceneItemId(id).WithSceneItemEnabled(enabled))
	return err
}

func (t *goobsTransport) ItemSourceSize(scene string, id int) (int, int, error) {
	resp, err := t.c.SceneItems.GetSceneItemTransform(sceneitems.NewGetSceneItemTransformParams().
		WithSceneName(scene).WithSceneItemId(id))
	if err != nil {
		return 0, 0, err
	}
	tf := resp.SceneItemTransform
	if tf == nil {
		return 0, 0, nil
	}
	return int(tf.SourceWidth), int(tf.SourceHeight), nil
}

func (t *goobsTransport) ItemTransform(scene string, id int) (renocore.Transform, error) {
	resp, err := t.c.SceneItems.GetSceneItemTransform(sceneitems.NewGetSceneItemTransformParams().
		WithSceneName(scene).WithSceneItemId(id))
	if err != nil {
		return renocore.Transform{}, err
	}
	tf := resp.SceneItemTransform
	if tf == nil {
		return renocore.Transform{}, nil
	}
	return renocore.Transform{
		X: tf.PositionX, Y: tf.PositionY,
		BoundsW: tf.BoundsWidth, BoundsH: tf.BoundsHeight,
		CropL: int(tf.CropLeft), CropT: int(tf.CropTop),
		CropR: int(tf.CropRight), CropB: int(tf.CropBottom),
		ScaleX: tf.ScaleX, ScaleY: tf.ScaleY,
	}, nil
}

// FilterKinds is the filter capability probe: nil (no error) when the
// server does not advertise the request at all, so callers degrade on
// one condition instead of two.
func (t *goobsTransport) FilterKinds() ([]string, error) {
	if !slices.Contains(t.reqs, "GetSourceFilterKindList") {
		return nil, nil
	}
	resp, err := t.c.Filters.GetSourceFilterKindList()
	if err != nil {
		return nil, err
	}
	return resp.SourceFilterKinds, nil
}

func (t *goobsTransport) SourceFilters(source string) ([]string, error) {
	resp, err := t.c.Filters.GetSourceFilterList(
		filters.NewGetSourceFilterListParams().WithSourceName(source))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(resp.Filters))
	for _, f := range resp.Filters {
		out = append(out, f.FilterName)
	}
	return out, nil
}

func (t *goobsTransport) RunFrameBatch(id string, reqs []renocore.FXRequest, budget time.Duration) error {
	return runFrameBatch(t.host, t.password, id, reqs, budget)
}

// SetSceneItemTransform anchors top-left (alignment 5) inside a
// scale-inner bounds box: aspect survives, slots stay put.
func (t *goobsTransport) SetSceneItemTransform(scene string, id int, tr renocore.Transform) error {
	want := &typedefs.SceneItemTransform{
		PositionX: tr.X, PositionY: tr.Y,
		Alignment:  5,
		BoundsType: "OBS_BOUNDS_SCALE_INNER", BoundsAlignment: 0,
		BoundsWidth: tr.BoundsW, BoundsHeight: tr.BoundsH,
		CropLeft: float64(tr.CropL), CropTop: float64(tr.CropT),
		CropRight: float64(tr.CropR), CropBottom: float64(tr.CropB),
	}
	// see Transform.ScaleX: OBS will not recompute a scale it derived from a
	// source that was not streaming yet, so send the answer when we have one
	if tr.ScaleX > 0 && tr.ScaleY > 0 {
		want.ScaleX, want.ScaleY = tr.ScaleX, tr.ScaleY
	}
	_, err := t.c.SceneItems.SetSceneItemTransform(sceneitems.NewSetSceneItemTransformParams().
		WithSceneName(scene).WithSceneItemId(id).WithSceneItemTransform(want))
	return err
}

func (t *goobsTransport) VirtualCamActive() (bool, error) {
	r, err := t.c.Outputs.GetVirtualCamStatus()
	if err != nil {
		return false, err
	}
	return r.OutputActive, nil
}

func (t *goobsTransport) StopVirtualCam() error {
	_, err := t.c.Outputs.StopVirtualCam()
	return err
}
