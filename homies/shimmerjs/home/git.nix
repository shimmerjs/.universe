{ pkgs, ... }:
{
  programs.git = with pkgs; {
    package = git;
    enable = true;

    userName = "alex weidner";
    userEmail = "shimmerjs@dpu.sh";

    extraConfig = {
      # Always checkout my forks branches instead of upstream
      checkout.defaultRemote = "origin";

      init = {
        defaultBranch = "main";
      };

      push = {
        # Don't require -u on pushes or setting up during git branch
        autoSetupRemote = "true";
        rebase = "false";
      };

      core = {
        editor = "vim";
      };
    };

    aliases = {
      c = "commit";
      co = "checkout";
    };
  };

  programs.zsh = {
    sessionVariables = {
      GIT_SSL_CAINFO = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
    };
    shellGlobalAliases = {
      # Naive git shell aliases for common workflows.
      gitsync = "git fetch upstream && git checkout main && git rebase upstream/main";
      batdiff = "git diff --name-only --diff-filter=d | xargs bat --diff";
    };
  };
}
