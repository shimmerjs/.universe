{
  pkgs,
  kitty,
  homeFiles,
  powerlevel10k,
  fonts,
}:
let
  py = pkgs.python3.withPackages (ps: [ ps.fonttools ]);
  startup = pkgs.writeText "kitty-startup.py" ''
    import os
    from pathlib import Path
    from kitty.config import load_config
    from kitty.launch import parse_launch_args
    from kitty.session import parse_session

    config_dir = Path("${homeFiles}/.config/kitty")
    bad_lines = []
    opts = load_config(str(config_dir / "kitty.conf"), accumulate_bad_lines=bad_lines)
    assert not bad_lines, bad_lines
    session_file = config_dir / "sessions/default.conf"
    assert opts.startup_session == str(session_file), opts.startup_session
    session = next(parse_session(session_file.read_text(), opts))
    expected = str(Path.home() / "dev")
    assert session.tabs and all(tab.windows for tab in session.tabs)
    for tab in session.tabs:
        for window in tab.windows:
            spec = window.launch_spec
            cwd = spec.opts.cwd if hasattr(spec, "opts") else spec.cwd
            assert cwd == expected, ("startup cwd", cwd, expected)

    binding = next(line.split(None, 2)[2] for line in
                   (config_dir / "kitty.conf").read_text().splitlines()
                   if line.startswith("map cmd+n "))
    command, *args = binding.split()
    assert command == "launch", binding
    launch = parse_launch_args(args)
    assert launch.opts.type == "os-window", launch.opts.type
    assert os.path.expanduser(launch.opts.cwd) == expected, launch.opts.cwd
    print("startup session and Cmd+N open ~/dev")
  '';
  glyphs = pkgs.writeText "prompt-glyphs.py" ''
    from pathlib import Path
    from fontTools.ttLib import TTFont

    family = next(line.removeprefix("font_family ") for line in
                  Path("${homeFiles}/.config/kitty/kitty.conf").read_text().splitlines()
                  if line.startswith("font_family "))
    cmaps = []
    for root in ${builtins.toJSON (map toString fonts)}:
        for path in Path(root).rglob("*.ttf"):
            with TTFont(path) as font:
                names = {n.toUnicode() for n in font["name"].names if n.nameID in (1, 16)}
                if family in names:
                    cmaps.append(font.getBestCmap())
    assert cmaps, ("configured font family is not installed", family)
    text = Path("prompt-glyphs.txt").read_text()
    assert text.strip(), "no prompt glyphs emitted"
    for char in text:
        if not char.isspace():
            assert all(ord(char) in cmap for cmap in cmaps), (family, hex(ord(char)))
    print("directory icons and prompt character exist in", family)
  '';
in
pkgs.runCommand "kitty-startup-check" { nativeBuildInputs = [ kitty pkgs.zsh py ]; } ''
  mkdir -p "$out"
  export LC_ALL=en_US.UTF-8
  export KITTY_CONFIG_DIRECTORY=${homeFiles}/.config/kitty
  kitty +runpy 'import runpy; runpy.run_path("${startup}", run_name="__main__")' > "$out/startup.txt"
  zsh -f <<'ZSH' > prompt-glyphs.txt
  source ${homeFiles}/.config/p10k.zsh
  source ${powerlevel10k}/internal/icons.zsh
  _p9k_init_icons
  for name in LOCK_ICON HOME_ICON HOME_SUB_ICON FOLDER_ICON; do
    print -r -- "''${(g::)icons[$name]}"
  done
  print -r -- "$POWERLEVEL9K_PROMPT_CHAR_OK_VIINS_CONTENT_EXPANSION"
  ZSH
  python3 ${glyphs} > "$out/glyphs.txt"
''
