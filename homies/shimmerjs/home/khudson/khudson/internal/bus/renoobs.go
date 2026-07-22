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

	"github.com/andreykaipov/goobs"
	"github.com/andreykaipov/goobs/api/requests/config"
	"github.com/andreykaipov/goobs/api/requests/scenes"

	"github.com/shimmerjs/khudson/khudson/internal/paths"
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
	return &goobsTransport{c: c, version: v.ObsVersion, reqs: v.AvailableRequests}, nil
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
