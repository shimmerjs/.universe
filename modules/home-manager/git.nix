{ pkgs, ... }:
{
  programs.git = with pkgs; {
    package = git;
    enable = true;

    userName = "alex weidner";

    extraConfig = {
      pull = {
        rebase = "false";
      };

      core = {
        editor = "vim";
      };
    };
  };
}
