package bus

import (
	"encoding/binary"
	"fmt"
	"log"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

// shedLoadFactor scales the core count into the shed threshold: a 1m
// loadavg above NumCPU*shedLoadFactor means the run queue has not been
// clearing for a sustained window, and khudson's own polling would
// compound the saturation, so non-essential polls skip until it drops.
const shedLoadFactor = 1.5

// shedCheckEvery bounds the loadavg read to one sysctl per interval
// rather than one per schedTick; darwin refreshes vm.loadavg on a ~5s
// kernel cadence, so finer sampling reads the same value.
const shedCheckEvery = 5 * time.Second

// shouldShed is the pure shed decision: skip non-essential polls when
// the 1-minute loadavg exceeds ncpu*shedLoadFactor.
func shouldShed(load1 float64, ncpu int) bool {
	return load1 > float64(ncpu)*shedLoadFactor
}

// essential marks a module whose polls must survive load shedding.
// Every polling widget sheds by default; a native module opts out only
// by implementing this marker (exec scrapes always shed).
type essential interface {
	Essential()
}

// sheddable reports whether a native widget's module polls skip under
// load shedding; unknown modules shed (pollNative would only burn a
// goroutine to error on them).
func (b *Bus) sheddable(name string) bool {
	m, ok := b.mods[name]
	if !ok {
		return true
	}
	_, ess := m.(essential)
	return !ess
}

// loadShedder caches the shed state between loadavg reads and logs only
// on state transitions, never per tick. Owned by the scheduler goroutine.
type loadShedder struct {
	shedding bool
	nextRead time.Time
	read     func() (float64, error) // test seam; nil means loadAvg1
}

// active reports whether this tick sheds non-essential polls.
func (s *loadShedder) active(now time.Time) bool {
	if now.Before(s.nextRead) {
		return s.shedding
	}
	s.nextRead = now.Add(shedCheckEvery)
	read := s.read
	if read == nil {
		read = loadAvg1
	}
	load1, err := read()
	if err != nil {
		// fail open: an unreadable loadavg must not stall the fleet
		if s.shedding {
			s.shedding = false
			log.Printf("khudson bus: load shed stopped (loadavg unreadable: %v)", err)
		}
		return false
	}
	ncpu := runtime.NumCPU()
	if shed := shouldShed(load1, ncpu); shed != s.shedding {
		s.shedding = shed
		if shed {
			log.Printf("khudson bus: load shed started: 1m loadavg %.2f > %d cpus * %.1f; skipping non-essential polls", load1, ncpu, shedLoadFactor)
		} else {
			log.Printf("khudson bus: load shed stopped: 1m loadavg %.2f", load1)
		}
	}
	return s.shedding
}

// loadAvg1 reads the 1-minute load average from the vm.loadavg sysctl:
// struct loadavg { fixpt_t ldavg[3]; long fscale; } -- three uint32
// fixed-point averages, 4 bytes padding, int64 scale. Mirrors
// module/cpumem's reader; the bus must not import a module package.
func loadAvg1() (float64, error) {
	raw, err := unix.SysctlRaw("vm.loadavg")
	if err != nil {
		return 0, fmt.Errorf("sysctl vm.loadavg: %w", err)
	}
	if len(raw) < 24 {
		return 0, fmt.Errorf("sysctl vm.loadavg: short read (%d bytes)", len(raw))
	}
	fscale := binary.LittleEndian.Uint64(raw[16:24])
	if fscale == 0 {
		return 0, fmt.Errorf("sysctl vm.loadavg: zero fscale")
	}
	return float64(binary.LittleEndian.Uint32(raw[0:4])) / float64(fscale), nil
}
