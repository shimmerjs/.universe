# main kitty integration

The bus needs RC access to the daily-driver kitty (kitty-sessions widget,
claude focus/resume verbs). This touches
`homies/shimmerjs/home/kitty/default.nix`, the shared always-on daily
config. Everything is folded into `module.nix` under
`universe.home.khudson.mainKittyIntegration.enable` -- off by default even
when the module is enabled, because it mutates the shared kitty config and
owes a manual relaunch (below). Enabled in production on aw-chainguard.

## posture: passwordless socket-only, by design

`allow_remote_control = mkForce "socket-only"` (over the daily config's
base `yes`): tty/escape-code RC is refused outright, and RC exists only on
the fixed socket. Auth is the socket file itself -- user-write-only under
the 0700 khudson state root. kitty never consults
`remote_control_password` for socket peers, so a password line is inert
posture-theater; the original password arc was built on that false premise
and never worked once from the launchd bus (setting KITTY_RC_PASSWORD
makes kitten demand a KITTY_PUBLIC_KEY that exists only inside kitty's own
children). Retired 2026-07-10: no rc-password.conf, no include, no
password code in the bus; posture-check legs (a)/(c) fail the build if any
password line regrows.

Security motivation that still stands: a predictable socket plus
`allow_remote_control yes` hands any local process send-text control of
the focused shell. socket-only plus the 0700-rooted socket narrows that to
same-user socket peers. Threat-model acceptance: same-user malware is
unchanged by any password scheme (a password file would be same-user-
readable anyway).

Actual bus verb surface on main-kitty.sock: `ls` (kittysessions widget,
claudeverb freshLS), `focus-window` (tap-to-focus), `launch` (resume).
Resume's consent gate is CLI-only reachability -- no panel row publishes
it.

## socket delivery: CLI --listen-on, never settings

Confirmed fatal: config-form unix `listen_on` gets `-<PID>` appended by
kitty (`expand_listen_on`, main.py ~365-371 in 0.47.4) unless the value
contains a literal `{kitty_pid}` -- the "fixed" path would never exist,
every relaunch would move the suffix, and SIGKILL would strand
`-<oldpid>` corpses. Only CLI `--listen-on` is verbatim. The module ships
it via `programs.kitty.darwinLaunchOptions`, rendered into
`~/.config/kitty/macos-launch-services-cmdline` and shlex-parsed on every
Launch-Services launch; the "Application Support" space needs the
embedded quotes because home-manager joins the list with bare spaces.
Accepted consequence: a shell-spawned `kitty` binds no socket -- the
daily kitty is LS-launched. The socket lives under the state root, not
/tmp (macOS reaps /private/tmp entries idle more than ~3 days).

## quick-access override

The quick-access terminal is a second kitty process on the same
kitty.conf; `kitty_override = "allow_remote_control=no"` means it never
binds `listen_on`, so it cannot squat or shadow the fixed socket and
exposes no second RC surface.

## caveats, stated

- RC posture and the socket bind at kitty startup only, and switch never
  restarts the LS-launched daily kitty: EVERY posture change re-opens an
  unbounded window where the running kitty keeps the old posture while
  build/check/activation report the new one. Human checklist after any
  RC-touching switch: quit+relaunch the daily kitty, then verify runtime:
  `ls "$HOME/Library/Application Support/khudson/main-kitty.sock"` and a
  passwordless
  `kitten @ --to "unix:$HOME/Library/Application Support/khudson/main-kitty.sock" ls`
  round-trip. Live right now: the RUNNING kitty still holds the
  password-era posture (switch replaces the conf file, not the process)
  until the relaunch owed at the next switch.
- Verify under socket-only (never confirmed): the `f2` cheatsheet and
  `cmd+shift+e` tab-switcher overlays use `launch --allow-remote-control`,
  which should keep working via the per-window KITTY_LISTEN_ON grant.
- Stale-socket handling on ungraceful exit is the bus's re-discovery
  job -- SHIPPED as internal/bus/mainkitty.go (30s probe, 2s dial bound,
  unlink only on ECONNREFUSED so a wedged-but-alive kitty never loses its
  socket, sticky "stale" state via ctl status). The locked decision
  survives: the config layer must not rm -f main-kitty.sock. Contrast:
  the substrate agent rm -f's its own kitty.sock pre-exec because it owns
  that path exclusively.
