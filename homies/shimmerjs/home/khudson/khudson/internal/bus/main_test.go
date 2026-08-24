package bus

import (
	"os"
	"testing"
)

// The reno verbs write state -- the overlay page, the canvas to go back to
// -- under the state root, and paths.Resolve reads $HOME rather than
// XDG_STATE_HOME, so a test that reached the real seam would be editing the
// machine it runs on. Redirecting renoStateDir for the WHOLE package makes
// that impossible to forget one test at a time; fakeRenoState still exists
// for the tests that need to read back what was written.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "khudson-bus-state")
	if err != nil {
		panic(err)
	}
	renoStateDir = func() (string, error) { return dir, nil }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
