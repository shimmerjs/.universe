# zsh rc posture pins on the RENDERED rc files, plus an isolated runtime
# init smoke. Catches the structural regression classes from the 2026-07
# audit: a second system-level compinit reappearing (two compinits with
# different fpaths fight over one .zcompdump and force full rebuilds per
# shell), the cached -C idiom lost, global command aliases coming back,
# and the keymap/matcher pins drifting. The smoke run proves the rc still
# initializes cleanly in isolation and that the smart-case completion
# style actually lands at runtime.
{
  pkgs,
  zshrc,
  etcZshrc,
}:
let
  etc = pkgs.writeText "etc-zshrc" etcZshrc;
in
pkgs.runCommand "zshrc-check" { nativeBuildInputs = [ pkgs.zsh ]; } ''
  rc=${zshrc}

  if grep -q 'autoload -U compinit' ${etc}; then
    echo "FAIL: /etc/zshrc runs compinit (double-compinit regression)"; exit 1
  fi
  grep -q 'autoload -U compinit' "$rc" || { echo "FAIL: user zshrc lost compinit"; exit 1; }
  grep -q 'compinit -C' "$rc" || { echo "FAIL: cached-compinit (-C) idiom gone"; exit 1; }

  grep -q 'bindkey -e' "$rc" || { echo "FAIL: emacs keymap pin gone (update this check if the keymap changes on purpose)"; exit 1; }
  grep -q 'matcher-list' "$rc" || { echo "FAIL: smart-case matcher-list zstyle gone"; exit 1; }
  if grep -qE '^alias -g (k|ksh|bazel|icat|tdot|idot)=' "$rc"; then
    echo "FAIL: command aliases are global again (mid-line expansion hazard)"; exit 1
  fi

  # runtime smoke, isolated from the build host: home-relative plugin
  # sources are [[ -f ]]-guarded away under the empty HOME; store-path
  # sources ride the rc's string context into this check's closure.
  # NON-interactive on purpose: `zsh -i` without a tty re-attaches for
  # compaudit's insecure-dirs prompt and ignores SIGTERM -- it wedged the
  # builder. Sourcing skips the prompt; KILL-timeout backstops any hang.
  export HOME=$TMPDIR ZDOTDIR=$TMPDIR TERM=dumb
  cp "$rc" "$ZDOTDIR/.zshrc"
  timeout -s KILL 60 zsh -f -c "source $ZDOTDIR/.zshrc 2> /dev/null; echo SOURCED_OK" < /dev/null \
    | grep -q SOURCED_OK || { echo "FAIL: rendered zshrc does not source (or hung)"; exit 1; }
  styles=$(timeout -s KILL 60 zsh -f -c "source $ZDOTDIR/.zshrc 2> /dev/null; zstyle -L ':completion:*'" < /dev/null)
  echo "$styles" | grep -q 'm:{a-z}' || { echo "FAIL: matcher-list not live at runtime"; exit 1; }

  touch $out
''
