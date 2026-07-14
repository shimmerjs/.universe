# launcher supervision fixes -- plan (post-incident 2026-07-14)

the 2026-07-14 cascade: hud.log shows 52 `launching HUD` lines against 3 `HUD exited` + 5 `torn down` -- one Run() loop logs an exit or teardown on every path between launches, so silent launch->launch runs mean the launcher *process* died and launchd (`KeepAlive = true`, module.nix:187, no ThrottleInterval) respawned it. every supervisor death orphaned the previous kitty + dock + module fleet (terminate() signals only cmd.Process, hudlaunch.go:183-196; no process group), stacking fullscreen HUD instances until the machine drowned. unplugging the Edge broke the loop because relaunch is display-gated. killer attribution (jetsam suspected) is still being verified by the forensics run, but these four fixes hold under any killer: they make the stack structurally impossible.

scope: fixes 1-4 only. the panel-side constant-cost work (zero transcript reads on the realtime path) is a separate arc.

## fix 1: process-group kill

the child must die with its supervisor's teardown, entire tree included.

- `runChild` (hudlaunch.go:134): set `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` so kitty and everything it spawns (dock, module subprocesses -- none call setsid) share one pgid.
- `terminate` (hudlaunch.go:183): signal the group, `syscall.Kill(-pgid, SIGTERM)`, then `SIGKILL` the group after killGrace. keep the existing single-process path as fallback when pgid lookup fails.
- edge case: signaling `-pgid` from the launcher is safe because the launcher itself never joins the child's group.

## fix 2: startup orphan sweep (pidfile, not pkill)

a fresh launcher must assume its predecessor died dirty and reap what it left.

- after `cmd.Start()`, write the child pgid to `<AppSupport>/khudson/hud-kitty.pid` (tmp+rename, hookspool's pattern); remove it after a reaped `cmd.Wait()`.
- at `Run()` entry: if the pidfile exists, verify the pid is alive AND its argv matches our kitty binary + our `--listen-on` socket (never kill by name or title -- the user runs real kitties). match -> SIGTERM the group, killGrace, SIGKILL, remove pidfile. stale/no-match -> just remove the pidfile.
- this also explains and retires the `Failed to finish decrypt` noise: an orphan holding (or having lost) the RC socket while a new kitty binds it. after the sweep, `sockclaim.Free` runs against a genuinely dead path.
- rejected alternative: `pkill -f khudson-hud` -- too blunt; title/argv collisions kill innocents.

## fix 3: singleton flock

- at `Run()` entry (inside hudlaunch, deliberately not main.go): `flock(LOCK_EX|LOCK_NB)` on `<AppSupport>/khudson/hud-launcher.lock`, held for process lifetime. on contention: log `another hud-launcher holds the lock` and exit. under KeepAlive the dead instance retries every throttle interval -- a no-op flock attempt, cheap.
- this is exactly the "flock'd sidecar" sockclaim.go's header says would close its probe-then-claim split-brain; the realistic second instance is a dev run racing the agent, which now fails loudly instead of racing.

## fix 4: backoff that survives respawn -- and signal-kill escalation

two defects: backoff state is in-memory (hudlaunch.go:69), so a respawned launcher relaunches at full speed; and `healthyRun` reset the backoff to 1s after the 40m29s run died mid-incident -- long-lived-then-SIGKILLed is system pressure, not health.

- persist `{lastLaunch, backoff}` to `<AppSupport>/khudson/hud-backoff.state` on every launch (tmp+rename). at `Run()` entry, load it; if `now < lastLaunch + backoff`, sleep the remainder before the first launch.
- escalate instead of reset when the child died by signal: `cmd.Wait()`'s `ExitError.Sys().(syscall.WaitStatus).Signaled()` distinguishes SIGKILL/SIGTERM death from clean exit. backoff resets only on clean exit or display-lost teardown; a signaled child doubles backoff even past healthyRun.
- launchd defense in depth: add `ThrottleInterval = 30` to the hud agent in module.nix so even a lost state file cannot tight-loop the supervisor. NOTE: module.nix is touched by uncommitted WIP -- this one-line edit needs coordination; everything else in this plan lands in hudlaunch.go/hudlaunch_test.go, which the WIP does not touch.

## tests + verification

- pure cores, fixture-style in hudlaunch_test.go: backoff schedule resume math (load/persist/remainder), pidfile verify-match (pid alive + argv match -> kill decision), signal-vs-clean exit classification.
- integration (gated like the existing toolchain-dependent tests): spawn a script that spawns a grandchild, terminate(), assert the whole group is dead; kill -9 the supervisor mid-run, restart Run(), assert the orphan is swept and exactly one child survives.
- live verification after switch: `kill -9` the running hud-launcher on the real system; expect launchd respawn, orphan sweep in hud.log, one relaunch after the persisted backoff, zero stacked kitties in `ps`.

## rollout

one change, ordered 3 -> 2 -> 1 -> 4 (lock first so the sweep never races a sibling; group-kill before backoff so escalation has clean semantics). deploy = commit + switch + `launchctl kickstart -k` of org.khudson.hud. hudlaunch.go and hudlaunch_test.go are clean of WIP; only the module.nix ThrottleInterval line conflicts.
