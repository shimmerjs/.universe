package socketio

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDecoderFrameBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        error
	}{
		{"oversize", strings.Repeat(" ", MaxFrameBytes) + "\n", FrameSizeError{}},
		{"truncated", `{"x":1}`, io.ErrUnexpectedEOF},
		{"empty", "", io.EOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			err := NewDecoder(strings.NewReader(tc.input)).Decode(&value)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	d := NewDecoder(strings.NewReader("1\n2\n"))
	for _, want := range []int{1, 2} {
		var got int
		if err := d.Decode(&got); err != nil || got != want {
			t.Fatalf("got %d err %v", got, err)
		}
	}
}
