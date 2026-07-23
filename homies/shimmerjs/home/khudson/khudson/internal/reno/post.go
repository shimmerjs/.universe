package reno

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Post ops: pure ffmpeg argv builders for the clip verbs (`khudson reno
// trim|stitch|mute|dub`). Clips from one OBS profile are homogeneous, so
// the concat demuxer stays lossless and audio ops are pure remux. Trim
// defaults to stream copy -- fast, but cuts SNAP TO KEYFRAMES; --exact
// re-encodes video for frame-accurate cuts. Builders never overwrite
// (no -y): the caller picks output names and a collision is an error,
// not a lost take.

// TrimArgs cuts [start, end] out of clip into out.
func TrimArgs(clip string, start, end time.Duration, out string, exact bool) ([]string, error) {
	if start < 0 || end <= start {
		return nil, fmt.Errorf("trim wants 0 <= start < end, got %v..%v", start, end)
	}
	if out == clip {
		return nil, errors.New("trim: output would overwrite the input")
	}
	argv := []string{"ffmpeg", "-nostdin",
		"-ss", fmtFF(start), "-to", fmtFF(end), "-i", clip}
	if exact {
		// frame-accurate: re-encode video, copy audio
		argv = append(argv, "-c:v", "libx264", "-preset", "fast", "-crf", "18", "-c:a", "copy")
	} else {
		argv = append(argv, "-c", "copy")
	}
	return append(argv, out), nil
}

// ConcatList is the ffconcat file body for stitching takes in order.
func ConcatList(clips []string) (string, error) {
	if len(clips) < 2 {
		return "", errors.New("stitch wants at least two clips")
	}
	var b strings.Builder
	b.WriteString("ffconcat version 1.0\n")
	for _, c := range clips {
		abs, err := filepath.Abs(c)
		if err != nil {
			return "", err
		}
		// ffconcat quoting: single-quoted, embedded quotes escaped
		fmt.Fprintf(&b, "file '%s'\n", strings.ReplaceAll(abs, "'", `'\''`))
	}
	return b.String(), nil
}

// StitchArgs concatenates the listfile's clips losslessly into out.
func StitchArgs(listFile, out string) []string {
	return []string{"ffmpeg", "-nostdin", "-f", "concat", "-safe", "0",
		"-i", listFile, "-c", "copy", out}
}

// MuteArgs drops the audio track: pure remux.
func MuteArgs(clip, out string) ([]string, error) {
	if out == clip {
		return nil, errors.New("mute: output would overwrite the input")
	}
	return []string{"ffmpeg", "-nostdin", "-i", clip, "-an", "-c:v", "copy", out}, nil
}

// DubArgs replaces the audio track with audio's: video remux, shortest
// stream wins so a long backing track cannot pad the clip.
func DubArgs(clip, audio, out string) ([]string, error) {
	if out == clip {
		return nil, errors.New("dub: output would overwrite the input")
	}
	return []string{"ffmpeg", "-nostdin", "-i", clip, "-i", audio,
		"-map", "0:v", "-map", "1:a", "-c:v", "copy", "-shortest", out}, nil
}

// DefaultOut suffixes the clip name: take1.mkv -> take1-trim.mkv.
func DefaultOut(clip, op string) string {
	ext := filepath.Ext(clip)
	return strings.TrimSuffix(clip, ext) + "-" + op + ext
}

// fmtFF renders a duration as ffmpeg's H:MM:SS.mmm.
func fmtFF(d time.Duration) string {
	ms := d.Milliseconds()
	return fmt.Sprintf("%d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
