{ pkgs, ... }:
{
  programs.git = with pkgs; {
    package = git;
    enable = true;

    settings = {
      user.name = "alex weidner";
      user.email = "shimmerjs@dpu.sh";

      core.editor = "vim";
      init.defaultBranch = "main";

      # Always checkout my forks branches instead of upstream
      checkout.defaultRemote = "origin";
      branch.sort = "-committerdate";
      commit.verbose = "true";
      diff.algorithm = "histogram";
      merge.conflictstyle = "zdiff3";
      # Don't require -u on pushes or setting up during git branch
      push.autosetupRemote = "true";
      push.rebase = "false";
      pull.rebase = "true";
      rebase.autosquash = "true";
      rebase.updateRefs = "true";
      rerere.enabled = "true";
      worktree.useRelativePaths = "true";

      status.aheadBehind = "false";
      fetch.output = "compact";

      alias = {
        c = "commit";
        cs = "commit -s";
        ammend = "commit --amend";
        fix = "commit --amend --no-edit";
        fixso = "commit --amend --no-edit --signoff";

        co = "checkout";
        b = "branch";
        d = "diff";
        p = "push";
        pl = "pull";
        s = "status --short --branch";

        sync = "!git checkout main && git pull upstream main";

        sls = "stash list";
        ss = "stash show";
        spop = "stash pop";
        sdrop = "stash drop";

        wls = "worktree list";
        wrm = "worktree remove";
        wprune = "worktree prune";
        wmv = "worktree move";
      };
    };
  };

  home = {
    shellAliases = {
      g = "git";
    };
    sessionVariables = {
      GIT_SSL_CAINFO = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
    };
  };
}
