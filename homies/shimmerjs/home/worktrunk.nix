{ inputs, lib, pkgs, ... }:
{
  imports = [ inputs.worktrunk.homeModules.default ];

  programs.worktrunk.enable = true;

  # PATH-level wt wrapper: a worktree-container dir (e.g. mono/) is not a repo
  # itself, so route through -C main when ./main is a worktree. The shell
  # integration resolves wt via PATH (WORKTRUNK_BIN unset), so interactive use
  # and direct/scripted invocations both go through this. hiPrio wins the
  # bin/wt collision against the package the module installs.
  home.packages = [
    (lib.hiPrio (pkgs.writeShellScriptBin "wt" ''
      real=${inputs.worktrunk.packages.${pkgs.stdenv.hostPlatform.system}.default}/bin/wt
      if [ "$1" != "-C" ] && [ -e main/.git ] && ! git rev-parse --git-dir >/dev/null 2>&1; then
        exec "$real" -C main "$@"
      fi
      exec "$real" "$@"
    ''))
  ];

  # One global layout rule: worktrees live next to the primary checkout,
  # named by branch.
  xdg.configFile."worktrunk/config.toml".text = ''
    worktree-path = "{{ repo_path }}/../{{ branch | sanitize }}"

    [aliases]
    ls = "wt list {{ args }}"
    rm = "wt remove {{ args }}"

    # up: rebase every worktree onto fresh upstream/main; skips detached or
    # mid-rebase worktrees, aborts (not stops) on conflict so no tree is left
    # in a half-rebased state.
    up = """
    git fetch upstream --prune && wt step for-each -- sh -c '
      git symbolic-ref -q HEAD >/dev/null || exit 0
      g=$(git rev-parse --git-dir)
      test -d "$g/rebase-merge" -o -d "$g/rebase-apply" && exit 0
      git update-index --refresh -q >/dev/null || true
      git rebase upstream/main --no-autostash || git rebase --abort
    '"""
  '';
}
