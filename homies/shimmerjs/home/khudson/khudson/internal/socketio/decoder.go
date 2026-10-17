package socketio

import (
	"bufio"
	"encoding/json"
	"io"
)

// ErrUncertainDelivery means the request may have reached the peer, but its
// outcome was not acknowledged. It must not be retried as a definite failure.
var ErrUncertainDelivery error = uncertainDelivery{}

type uncertainDelivery struct{}

func (uncertainDelivery) Error() string { return "request outcome unknown" }
func (uncertainDelivery) Kind() string  { return "uncertain_delivery" }

// MaxFrameBytes bounds one NDJSON message, including its newline.
const MaxFrameBytes = 8 << 20
const maxPendingBytes = 16 << 20

type FrameSizeError struct{}

func (FrameSizeError) Error() string { return "socket JSON frame exceeds size limit" }
func (FrameSizeError) Kind() string  { return "frame_too_large" }

// Decoder bounds each newline-delimited JSON message independently.
// A partial final line is a truncated frame, even if its JSON is valid.
type Decoder struct{ reader *bufio.Reader }

func NewDecoder(r io.Reader) *Decoder { return &Decoder{reader: bufio.NewReader(r)} }
func (d *Decoder) Decode(value any) error {
	var frame []byte
	for {
		fragment, err := d.reader.ReadSlice('\n')
		if len(frame)+len(fragment) > MaxFrameBytes {
			return FrameSizeError{}
		}
		frame = append(frame, fragment...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && len(frame) != 0 {
			return io.ErrUnexpectedEOF
		}
		if err != nil {
			return err
		}
		return json.Unmarshal(frame, value)
	}
}
