# main kitty integration

The bus needs RC access to the daily-driver kitty (kitty-sessions widget,
send actions). This touches `homies/shimmerjs/home/kitty/default.nix`, the
shared always-on daily config.

The diffs below (sections 1 and 3) are now folded into `module.nix` under
`universe.home.khudson.mainKittyIntegration.enable` (off by default even when
the module is enabled): `allow_remote_control = mkForce "socket-only"`, the
fixed socket, the quick-access RC-off override, and the `include
rc-password.conf` line. The RC PASSWORD ITSELF (section 2) stays hand-applied:
a literal in nix lands world-readable in the store, so `rc-password.conf` is a
hand-created 0600 non-store include.

CORRECTION (confirmed fatal): the socket must NOT be
delivered as `settings.listen_on`. Config-form unix sockets get `-<PID>`
appended by kitty (`expand_listen_on`, main.py:409-410 in 0.47.4) unless the
value contains a literal `{kitty_pid}` -- so the "fixed" path would never
exist, every relaunch would move the suffix, and SIGKILL would strand stale
`-<oldpid>` sockets that the bus must not clean (see caveats). Only CLI
`--listen-on` is verbatim. The module therefore ships it via
`programs.kitty.darwinLaunchOptions`, which home-manager renders into
`~/.config/kitty/macos-launch-services-cmdline`; kitty's launcher shlex-parses
that file on every Launch-Services launch. The path's space ("Application
Support") is embedded-quoted because home-manager joins the list with bare
spaces. Consequence, accepted: a shell-spawned `kitty` reads no
launch-services cmdline and binds no socket -- the daily kitty is LS-launched.

Security rationale: a predictable world-readable socket combined
with the current `allow_remote_control yes` hands any local process
send-text control of the daily terminal -- arbitrary input injection into
whatever shell is focused. `socket-only` plus `remote_control_password`
narrows that to password-holders on one known socket, and the quick-access
override keeps the second kitty process on the same kitty.conf from exposing
an unhardened copy of it.

## 1. settings diff in `programs.kitty.settings`

```nix
       settings = {
         # Ensure that Nix-managed binaries are available to kitty actions
         env = "PATH=${config.home.profileDirectory}/bin:/run/current-system/sw/bin:$PATH";
-        # Required to automate kitty
-        allow_remote_control = "yes";
+        # khudson: RC only via the fixed socket below; tty RC is refused, so
+        # programs inside windows cannot drive kitty without the password.
+        allow_remote_control = "socket-only";
+        # khudson: fixed, non-pid socket ({kitty_pid} globs to
+        # >=2 sockets because quick-access is a second kitty process; a fixed
+        # path is a well-defined referent for the bus). Out of /tmp (the
+        # macOS reaper). Env vars are expanded by kitty.
+        listen_on = "unix:\${HOME}/Library/Application Support/khudson/main-kitty.sock";
```

## 2. RC password stub in `programs.kitty.extraConfig`

A literal password in nix lands world-readable in the store, so the real
line lives in a user-owned, non-store include:

```nix
+    # khudson: RC password, kept out of the store. rc-password.conf is a
+    # hand-created 0600 file; a missing include is ignored with only a
+    # warning, so first activation does not break kitty startup.
+    extraConfig = ''
+      include rc-password.conf
+    '';
```

One-time, by hand (password value of your choosing; verbs are the M9 budgeted
set the bus actually uses on the main socket):

```sh
umask 177
cat > ~/.config/kitty/rc-password.conf <<'EOF'
remote_control_password "CHANGE-ME" ls focus-window focus-tab send-text
EOF
```

The bus reads the same file (or the Keychain, clod pattern) to
authenticate; the password never rides an env var.

## 3. quick-access RC-off override

The quick-access terminal is a second kitty process on the same kitty.conf.
With RC off it never binds `listen_on`, so it cannot squat or shadow the
fixed socket and exposes no second RC surface:

```nix
     quickAccessTerminalConfig = {
       edge = "left";
       columns = "180";
       hide_on_focus_loss = "yes";
       background_opacity = "0.9";
+      # khudson: no RC surface on the quick-access instance.
+      # RC off also means its listen_on is never bound, so the fixed
+      # main-kitty.sock stays scoped to the main instance.
+      kitty_override = "allow_remote_control=no";
     };
```

## caveats, stated

- The socket and `allow_remote_control` bind at kitty startup only, and
  switch never restarts the LS-launched daily kitty -- so this is NOT a
  one-time cost: EVERY future change
  to the RC posture or launch options re-opens an unbounded window where the
  running kitty keeps the old posture while build/check/activation all
  report the new one. Nothing in home-manager can close it. Mitigation is
  this human checklist, run after any switch that touched the RC surface:
  quit+relaunch the daily kitty, then verify the RUNTIME state, not the
  rendered config:
  `ls "$HOME/Library/Application Support/khudson/main-kitty.sock"` and a
  `kitten @ --to "unix:$HOME/Library/Application Support/khudson/main-kitty.sock" ls`
  round-trip (with the password from rc-password.conf).
- Verify after applying: the `f2` keybindings-cheatsheet and `cmd+shift+e`
  tab-switcher overlays use `launch --allow-remote-control`, which under
  `socket-only` should keep working via the per-window KITTY_LISTEN_ON
  grant -- confirm both before calling this done.
- Stale-socket handling on ungraceful exit (SIGKILL leaves the unix socket
  inode behind) is the bus's re-discovery problem; do not add rm -f hacks to
  the main kitty config.
