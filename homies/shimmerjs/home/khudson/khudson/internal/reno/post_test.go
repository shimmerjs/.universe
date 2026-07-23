package reno

import (
	"strings"
	"testing"
	"time"
)

func TestTrimArgs(t *testing.T) {
	argv, err := TrimArgs("take1.mkv", 5*time.Second, 95*time.Second+500*time.Millisecond, "out.mkv", false)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(argv, " ")
	want := "ffmpeg -nostdin -ss 0:00:05.000 -to 0:01:35.500 -i take1.mkv -c copy out.mkv"
	if got != want {
		t.Errorf("argv = %q\nwant  %q", got, want)
	}

	argv, _ = TrimArgs("take1.mkv", 0, time.Second, "out.mkv", true)
	if !strings.Contains(strings.Join(argv, " "), "-c:v libx264") {
		t.Errorf("exact trim must re-encode: %v", argv)
	}

	if _, err := TrimArgs("a.mkv", 5*time.Second, 2*time.Second, "o.mkv", false); err == nil {
		t.Error("end before start must error")
	}
	if _, err := TrimArgs("a.mkv", 0, time.Second, "a.mkv", false); err == nil {
		t.Error("in-place trim must refuse")
	}
}

func TestConcatAndStitch(t *testing.T) {
	list, err := ConcatList([]string{"/w/take1.mkv", "/w/it's.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(list, "ffconcat version 1.0\n") ||
		!strings.Contains(list, "file '/w/take1.mkv'\n") ||
		!strings.Contains(list, `file '/w/it'\''s.mkv'`) {
		t.Errorf("list = %q", list)
	}
	if _, err := ConcatList([]string{"one.mkv"}); err == nil {
		t.Error("single-clip stitch must error")
	}
	got := strings.Join(StitchArgs("/tmp/l.ffconcat", "out.mkv"), " ")
	if got != "ffmpeg -nostdin -f concat -safe 0 -i /tmp/l.ffconcat -c copy out.mkv" {
		t.Errorf("stitch argv = %q", got)
	}
}

func TestMuteDubDefaults(t *testing.T) {
	argv, _ := MuteArgs("a.mkv", "b.mkv")
	if got := strings.Join(argv, " "); got != "ffmpeg -nostdin -i a.mkv -an -c:v copy b.mkv" {
		t.Errorf("mute argv = %q", got)
	}
	argv, _ = DubArgs("a.mkv", "v.m4a", "b.mkv")
	if got := strings.Join(argv, " "); !strings.Contains(got, "-map 0:v -map 1:a -c:v copy -shortest") {
		t.Errorf("dub argv = %q", got)
	}
	if _, err := MuteArgs("a.mkv", "a.mkv"); err == nil {
		t.Error("in-place mute must refuse")
	}
	if got := DefaultOut("/w/take1.mkv", "trim"); got != "/w/take1-trim.mkv" {
		t.Errorf("DefaultOut = %q", got)
	}
}
