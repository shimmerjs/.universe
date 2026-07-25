# bat with CUE highlighting. bat 0.26 ships no cuelang syntax and there is
# no upstream .sublime-syntax to vendor (the ones published under "cue" are
# CD cue sheets), so ./cue.sublime-syntax is ours. Home Manager rebuilds the
# syntect cache on activation, so a change here lands on switch.
{
  programs.bat = {
    enable = true;
    syntaxes.cue = {
      src = ./.;
      file = "cue.sublime-syntax";
    };
  };
}
