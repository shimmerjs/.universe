package bus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSID = "aaaaaaaa-1111-2222-3333-444444444444"

// lsFixture builds one `kitten @ ls` JSON tree: three windows, the claude
// process NOT fg[0] (caffeinate/-zsh lead the group in captured data).
func lsFixture(userVarSID string) string {
	uv := ""
	if userVarSID != "" {
		uv = fmt.Sprintf(`"claude_session": %q`, userVarSID)
	}
	return fmt.Sprintf(`[
	  {"id": 1, "tabs": [
	    {"id": 1, "windows": [
	      {"id": 11, "pid": 100, "user_vars": {},
	       "foreground_processes": [{"pid": 100, "cmdline": ["-zsh"]}]},
	      {"id": 12, "pid": 200, "user_vars": {%s},
	       "foreground_processes": [
	         {"pid": 201, "cmdline": ["caffeinate"]},
	         {"pid": 202, "cmdline": ["-zsh"]},
	         {"pid": 4242, "cmdline": ["claude"]}]},
	      {"id": 13, "pid": 300, "user_vars": {},
	       "foreground_processes": [{"pid": 300, "cmdline": ["-zsh"]}]}
	    ]}
	  ]}
	]`, uv)
}

// fakeKitten records calls and serves canned responses per leading arg.
type fakeKitten struct {
	calls [][]string
	pw    []string
	ls    string
	errOn string // leading arg to fail on
}

func (f *fakeKitten) run(_ context.Context, _, password string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	f.pw = append(f.pw, password)
	if f.errOn != "" && args[0] == f.errOn {
		return nil, errors.New("boom")
	}
	if args[0] == "ls" {
		return []byte(f.ls), nil
	}
	return nil, nil
}

func (f *fakeKitten) argv(i int) string { return strings.Join(f.calls[i], " ") }

// verbs builds a wired ClaudeVerbs over temp dirs.
func verbs(t *testing.T, f *fakeKitten, rcConf string) *ClaudeVerbs {
	t.Helper()
	dir := t.TempDir()
	pwFile := filepath.Join(dir, "rc-password.conf")
	if rcConf != "" {
		if err := os.WriteFile(pwFile, []byte(rcConf), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &ClaudeVerbs{
		Socket:       filepath.Join(dir, "main-kitty.sock"),
		PasswordFile: pwFile,
		SpoolDir:     filepath.Join(dir, "spool"),
		SessionsDir:  filepath.Join(dir, "sessions"),
		LogPath:      filepath.Join(dir, "log", "claude-verbs.log"),
		Run:          f.run,
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func regEntry(pid int, sid string, updatedAt int64) string {
	return fmt.Sprintf(`{"pid":%d,"sessionId":%q,"updatedAt":%d}`, pid, sid, updatedAt)
}

func logBody(t *testing.T, v *ClaudeVerbs) string {
	t.Helper()
	b, err := os.ReadFile(v.LogPath)
	if err != nil {
		return ""
	}
	return string(b)
}

const rcM9 = "remote_control_password \"sekrit\" ls focus-window focus-tab send-text\n"

func TestFocusViaUserVar(t *testing.T) {
	f := &fakeKitten{ls: lsFixture(testSID)}
	v := verbs(t, f, rcM9)
	if err := v.Focus(context.Background(), testSID); err != nil {
		t.Fatalf("Focus: %v", err)
	}
	if len(f.calls) != 2 || f.argv(0) != "ls" || f.argv(1) != "focus-window --match id:12" {
		t.Fatalf("calls = %v, want fresh ls then focus-window", f.calls)
	}
	// the password rides env-only via the runner param, never argv
	if f.pw[0] != "sekrit" || f.pw[1] != "sekrit" {
		t.Errorf("passwords = %v", f.pw)
	}
	if log := logBody(t, v); !strings.Contains(log, "ok -> window 12 via user-var") {
		t.Errorf("log = %q", log)
	}
	if strings.Contains(logBody(t, v), "sekrit") {
		t.Error("password leaked into the log")
	}
}

// Spool kitty_window_id is accepted only when the window's fg pids still
// include the registry pid (pid revalidation); otherwise the chain falls
// through to the registry-pid scan over ALL fg pids.
func TestFocusViaSpoolWindowRevalidated(t *testing.T) {
	f := &fakeKitten{ls: lsFixture("")}
	v := verbs(t, f, rcM9)
	writeFile(t, filepath.Join(v.SpoolDir, testSID+".json"),
		fmt.Sprintf(`{"session_id":%q,"kitty_window_id":"12"}`, testSID))
	writeFile(t, filepath.Join(v.SessionsDir, "4242.json"), regEntry(4242, testSID, 2))
	if err := v.Focus(context.Background(), testSID); err != nil {
		t.Fatalf("Focus: %v", err)
	}
	if f.argv(1) != "focus-window --match id:12" {
		t.Fatalf("calls = %v", f.calls)
	}
	if log := logBody(t, v); !strings.Contains(log, "via spool-window") {
		t.Errorf("log = %q", log)
	}
}

func TestFocusSpoolWindowStaleFallsToRegistryScan(t *testing.T) {
	f := &fakeKitten{ls: lsFixture("")}
	v := verbs(t, f, rcM9)
	// spool points at window 11, whose fg pids do NOT include the claude pid
	writeFile(t, filepath.Join(v.SpoolDir, testSID+".json"),
		fmt.Sprintf(`{"session_id":%q,"kitty_window_id":11}`, testSID))
	// two registry files for the sid: newest updatedAt wins
	writeFile(t, filepath.Join(v.SessionsDir, "9999.json"), regEntry(9999, testSID, 1))
	writeFile(t, filepath.Join(v.SessionsDir, "4242.json"), regEntry(4242, testSID, 2))
	if err := v.Focus(context.Background(), testSID); err != nil {
		t.Fatalf("Focus: %v", err)
	}
	// pid 4242 sits LAST in window 12's fg group; the scan must not stop at fg[0]
	if f.argv(1) != "focus-window --match id:12" {
		t.Fatalf("calls = %v", f.calls)
	}
	if log := logBody(t, v); !strings.Contains(log, "via registry-pid") {
		t.Errorf("log = %q", log)
	}
}

func TestFocusMissLogs(t *testing.T) {
	f := &fakeKitten{ls: lsFixture("")}
	v := verbs(t, f, rcM9)
	err := v.Focus(context.Background(), testSID)
	if err == nil {
		t.Fatal("Focus(miss): want error, got nil")
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %v, want ls only on a miss", f.calls)
	}
	if log := logBody(t, v); !strings.Contains(log, "MISS") || !strings.Contains(log, testSID) {
		t.Errorf("log = %q, want the miss recorded (handleRowAct discards exit status)", log)
	}
}

func TestFocusLSFailureLogs(t *testing.T) {
	f := &fakeKitten{ls: lsFixture(""), errOn: "ls"}
	v := verbs(t, f, rcM9)
	if err := v.Focus(context.Background(), testSID); err == nil {
		t.Fatal("Focus(ls error): want error, got nil")
	}
	if log := logBody(t, v); !strings.Contains(log, "ls: boom") {
		t.Errorf("log = %q", log)
	}
}

// Resume is staged: without `launch` in the M9 allowlist it must not touch
// the socket, and the staged state lands in the log.
func TestResumeStagedWithoutLaunchVerb(t *testing.T) {
	f := &fakeKitten{ls: lsFixture("")}
	v := verbs(t, f, rcM9)
	writeFile(t, filepath.Join(v.SpoolDir, testSID+".json"),
		fmt.Sprintf(`{"session_id":%q,"workspace":{"current_dir":"/x/foo"}}`, testSID))
	err := v.Resume(context.Background(), testSID, "")
	if err == nil || !strings.Contains(err.Error(), "staged") {
		t.Fatalf("Resume = %v, want the staged error", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls = %v, want none while staged", f.calls)
	}
	log := logBody(t, v)
	for _, want := range []string{"staged", "launch", "relaunch"} {
		if !strings.Contains(log, want) {
			t.Errorf("log = %q, want %q in the staged record", log, want)
		}
	}
}

func TestResumeLaunchesWhenAllowed(t *testing.T) {
	conf := "remote_control_password \"sekrit\" ls focus-window focus-tab send-text launch\n"
	f := &fakeKitten{ls: lsFixture("")}
	v := verbs(t, f, conf)
	writeFile(t, filepath.Join(v.SpoolDir, testSID+".json"),
		fmt.Sprintf(`{"session_id":%q,"workspace":{"current_dir":"/x/foo"}}`, testSID))
	if err := v.Resume(context.Background(), testSID, ""); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	want := "launch --type tab --cwd /x/foo --var claude_session=" + testSID +
		" claude --resume " + testSID
	if len(f.calls) != 2 || f.argv(0) != "ls" || f.argv(1) != want {
		t.Fatalf("calls = %v, want [ls, %q]", f.calls, want)
	}
}

// A still-running session is focused, never duplicated (revalidate-at-exec).
func TestResumeFocusesRunningSession(t *testing.T) {
	conf := "remote_control_password \"sekrit\" launch\n"
	f := &fakeKitten{ls: lsFixture(testSID)}
	v := verbs(t, f, conf)
	writeFile(t, filepath.Join(v.SpoolDir, testSID+".json"),
		fmt.Sprintf(`{"session_id":%q,"cwd":"/x/foo"}`, testSID))
	if err := v.Resume(context.Background(), testSID, ""); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if f.argv(1) != "focus-window --match id:12" {
		t.Fatalf("calls = %v, want focus over duplicate launch", f.calls)
	}
}

func TestResumeNeedsSpoolCwd(t *testing.T) {
	conf := "remote_control_password \"sekrit\"\n" // no verb list = all allowed
	f := &fakeKitten{ls: lsFixture("")}
	v := verbs(t, f, conf)
	err := v.Resume(context.Background(), testSID, "")
	if err == nil || !strings.Contains(err.Error(), "spool-backed") {
		t.Fatalf("Resume(no cwd) = %v, want the spool-backed refusal", err)
	}
	// an explicit cwd argument overrides the missing spool
	if err := v.Resume(context.Background(), testSID, "/x/given"); err != nil {
		t.Fatalf("Resume(explicit cwd): %v", err)
	}
	if got := f.argv(1); !strings.Contains(got, "--cwd /x/given") {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestRegistryPID(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "100.json"), regEntry(100, testSID, 1))
	writeFile(t, filepath.Join(dir, "200.json"), regEntry(200, testSID, 5))
	writeFile(t, filepath.Join(dir, "300.json"), regEntry(300, "other-sid", 9))
	writeFile(t, filepath.Join(dir, "bad.json"), "{nope")
	// pid absent from the record body: the filename stem stands in
	writeFile(t, filepath.Join(dir, "400.json"),
		fmt.Sprintf(`{"sessionId":%q,"updatedAt":7}`, testSID))
	if got := registryPID(dir, testSID); got != 400 {
		t.Errorf("registryPID = %d, want newest updatedAt (400)", got)
	}
	if got := registryPID(dir, "missing"); got != 0 {
		t.Errorf("registryPID(missing) = %d, want 0", got)
	}
	if got := registryPID(filepath.Join(dir, "nope"), testSID); got != 0 {
		t.Errorf("registryPID(missing dir) = %d, want 0", got)
	}
	if got := registryPID("", testSID); got != 0 {
		t.Errorf("registryPID(\"\") = %d, want 0", got)
	}
}

func TestSpoolWindowIDShapes(t *testing.T) {
	f := &fakeKitten{}
	v := verbs(t, f, "")
	if got := v.spoolWindowID(testSID); got != 0 {
		t.Errorf("spoolWindowID(no spool) = %d", got)
	}
	writeFile(t, filepath.Join(v.SpoolDir, testSID+".json"), `{"kitty_window_id":"17"}`)
	if got := v.spoolWindowID(testSID); got != 17 {
		t.Errorf("spoolWindowID(string) = %d, want 17", got)
	}
	writeFile(t, filepath.Join(v.SpoolDir, testSID+".json"), `{"kitty_window_id":18}`)
	if got := v.spoolWindowID(testSID); got != 18 {
		t.Errorf("spoolWindowID(number) = %d, want 18", got)
	}
	writeFile(t, filepath.Join(v.SpoolDir, testSID+".json"), `{"kitty_window_id":"junk"}`)
	if got := v.spoolWindowID(testSID); got != 0 {
		t.Errorf("spoolWindowID(junk) = %d, want 0", got)
	}
}
