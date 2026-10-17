package bus

import "context"

// notifyRelaysLocked coalesces connection changes; the worker reads current
// demand under mu, so a full wake channel cannot lose the final state.
func (b *Bus) notifyRelaysLocked() {
	select {
	case b.relayWake <- struct{}{}:
	default:
	}
}

func (b *Bus) runRelays(ctx context.Context, runners [3]func(context.Context)) {
	type reader struct {
		cancel context.CancelFunc
		done   chan struct{}
	}
	var readers [3]reader
	stop := func(i int) {
		if r := readers[i]; r.cancel != nil {
			r.cancel()
			<-r.done
			readers[i] = reader{}
		}
	}
	defer func() {
		for _, r := range readers {
			if r.cancel != nil {
				r.cancel()
			}
		}
		for i := range readers {
			stop(i)
		}
	}()
	for {
		b.mu.Lock()
		demand := b.relayDemandLocked()
		b.mu.Unlock()
		for i, wanted := range demand {
			if !wanted || ctx.Err() != nil {
				stop(i)
				continue
			}
			if readers[i].cancel == nil {
				child, cancel := context.WithCancel(ctx)
				done := make(chan struct{})
				readers[i] = reader{cancel, done}
				go func() { defer close(done); runners[i](child) }()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-b.relayWake:
		}
	}
}
