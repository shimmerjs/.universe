package module

// Ring is a fixed-capacity ring buffer of normalized samples backing series
// history. Make one with NewRing; the zero value has no capacity.
type Ring struct {
	buf   []float64
	start int
	n     int
}

// NewRing returns an empty ring holding at most n samples.
func NewRing(n int) *Ring { return &Ring{buf: make([]float64, n)} }

// Push appends v, evicting the oldest sample once full.
func (r *Ring) Push(v float64) {
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = v
		r.n++
		return
	}
	r.buf[r.start] = v
	r.start = (r.start + 1) % len(r.buf)
}

// Samples returns a copy of the buffered history, oldest first, so callers
// may keep pushing while an emitted slice is in flight.
func (r *Ring) Samples() []float64 {
	out := make([]float64, r.n)
	for i := range out {
		out[i] = r.buf[(r.start+i)%len(r.buf)]
	}
	return out
}
