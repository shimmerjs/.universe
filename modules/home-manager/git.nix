{ pkgs, ... }:
{
  programs.git = with pkgs; {
    package = git;
    enable = true;

    userName = "alex weidner";
    userEmail = "shimmerjs@dpu.sh";

    extraConfig = {
      init = {
        defaultBranch = "main";
      };

      pull = {
        rebase = "false";
      };

      core = {
        editor = "vim";
      };
    };
  };
}
