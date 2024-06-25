# Default configuration for personal devices.
{ pkgs, ... }:
{
  programs.git = with pkgs; {
    userEmail = "shimmerjs@dpu.sh";
  };
}
