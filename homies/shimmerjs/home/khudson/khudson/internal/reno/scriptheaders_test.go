package reno

import (
	"strings"
	"testing"
)

// The recording-config headers (screen/audio/webcam) are record-gated
// like scene, exclusive with scene (the config path owns the scene),
// and land on Script.RecordCfg -- the same core the ad hoc surfaces
// drive.
func TestRecordConfigHeaders(t *testing.T) {
	src := []byte(`demo talk
record
screen dell
screen built-in 0,0,1512x982
audio none
webcam br
step intro
`)
	s, err := Parse("", src)
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.RecordCfg
	if len(cfg.Screens) != 2 || cfg.Screens[0].Display != "dell" {
		t.Errorf("screens = %+v", cfg.Screens)
	}
	if cfg.Screens[1].Region == nil || cfg.Screens[1].Region.W != 1512 {
		t.Errorf("region = %+v", cfg.Screens[1].Region)
	}
	if cfg.Audio != AudioNone || cfg.Webcam != "br" {
		t.Errorf("audio/webcam = %q/%q", cfg.Audio, cfg.Webcam)
	}
}

func TestRecordConfigHeaderVet(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"config without record", "demo d\nscreen 1\nstep s\n", "without a record header"},
		{"scene and config exclusive", "demo d\nrecord\nscene main\nscreen 1\nstep s\n", "exclusive"},
		{"bad region", "demo d\nrecord\nscreen 1 nope\nstep s\n", "region"},
		{"duplicate audio", "demo d\nrecord\naudio a\naudio b\nstep s\n", "duplicate audio"},
		{"bad webcam", "demo d\nrecord\nwebcam sideways\nstep s\n", "webcam"},
		{"screen in a step", "demo d\nrecord\nstep s\nscreen 1\n", "header, not a step command"},
		{"audio in a step", "demo d\nrecord\nstep s\naudio none\n", "header, not a step command"},
	}
	for _, c := range cases {
		_, err := Parse("", []byte(c.src))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// cam FX are step commands, gated twice: the script must record, and it
// must stage a webcam for them to animate.
func TestCamStepCommand(t *testing.T) {
	src := []byte(`demo talk
record
webcam br
step intro
cam move big 600ms bounce
cam hide
`)
	s, err := Parse("", src)
	if err != nil {
		t.Fatal(err)
	}
	cmds := s.Steps[0].Commands
	if len(cmds) != 2 || cmds[0].Name != "cam" {
		t.Fatalf("commands = %+v", cmds)
	}
	fx, err := ParseCamFX(cmds[0].Args)
	if err != nil {
		t.Fatal(err)
	}
	if fx.Kind != FXMove || fx.Preset != "big" || fx.Ease != "bounce" {
		t.Errorf("fx = %+v", fx)
	}
}

func TestCamStepVet(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"no record", "demo d\nstep s\ncam hide\n", "never records"},
		{"no webcam header", "demo d\nrecord\nstep s\ncam hide\n", "stages no webcam"},
		{"bad kind", "demo d\nrecord\nwebcam br\nstep s\ncam wiggle\n", "wants one of"},
		{"bad ease", "demo d\nrecord\nwebcam br\nstep s\ncam move big fast\n", "ease"},
		{"off is not a target", "demo d\nrecord\nwebcam br\nstep s\ncam move off\n", "cam target"},
	}
	for _, c := range cases {
		_, err := Parse("", []byte(c.src))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}
