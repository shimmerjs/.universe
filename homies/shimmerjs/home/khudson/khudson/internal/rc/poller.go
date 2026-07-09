package rc

import "sync"

// TextPoller serializes the scrape path: at most one in-flight
// get-text per target, with a trailing-edge reschedule when requests land
// mid-flight. A drag otherwise pumps megabytes of ANSI through the RC
// socket into the kitty rendering the glass.
type TextPoller struct {
	Client *Client

	mu     sync.Mutex
	states map[string]*pollState
}

type pollState struct {
	inflight bool
	pending  bool
	opts     GetTextOpts
	sink     func(text string, err error)
}

// NewTextPoller wraps c.
func NewTextPoller(c *Client) *TextPoller {
	return &TextPoller{Client: c, states: make(map[string]*pollState)}
}

// Request schedules one get-text for opts.Match. If a poll for the same
// match is in flight, the newest opts and sink win and exactly one more
// poll runs after the current one returns.
func (p *TextPoller) Request(opts GetTextOpts, sink func(text string, err error)) {
	p.mu.Lock()
	st, ok := p.states[opts.Match]
	if !ok {
		st = &pollState{}
		p.states[opts.Match] = st
	}
	st.opts, st.sink = opts, sink
	if st.inflight {
		st.pending = true
		p.mu.Unlock()
		return
	}
	st.inflight = true
	p.mu.Unlock()
	go p.run(st)
}

func (p *TextPoller) run(st *pollState) {
	for {
		p.mu.Lock()
		opts, sink := st.opts, st.sink
		p.mu.Unlock()

		text, err := p.Client.GetText(opts)
		if sink != nil {
			sink(text, err)
		}

		p.mu.Lock()
		if st.pending {
			st.pending = false
			p.mu.Unlock()
			continue
		}
		// terminal: release the entry -- match keys are per-window-id, so a
		// kept map grows for the bus lifetime. A racing Request simply
		// re-creates a fresh entry.
		delete(p.states, st.opts.Match)
		p.mu.Unlock()
		return
	}
}
