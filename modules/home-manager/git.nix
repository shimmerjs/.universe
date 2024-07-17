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

      pull = {
        # Don't require -u on pushes or setting up during git branch
        autoSetupRemote = "true";
        rebase = "false";
      };

      core = {
        editor = "vim";
      };
    };
  };
}
