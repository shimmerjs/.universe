# Nerd Font glyph pin for the dock's icons. Every `"\U000FXXXX" // nf-<name>`
# in the Go sources is resolved against the font the HOST installs, and the
# font's own glyph name for that codepoint must equal the comment's.
#
# It catches two failures, and the second is the reason this exists. A
# codepoint the font does not carry renders as tofu, which is at least
# visible. A codepoint the font carries as something ELSE renders a
# confident wrong icon: the OBS-window tile shipped U+F05E7 commented
# nf-md-window_restore, and U+F05E7 is md-cursor_text. Nothing on glass says
# so -- a text cursor is a plausible-looking button -- and the comment reads
# correct while lying.
#
# The check is only as good as its anchor, so it fails when it finds NO
# escapes at all rather than passing vacuously on a bad regex.
{
  pkgs,
  fonts,
  src,
}:
let
  # ttf, not otf: the Nerd Font patcher emits both for some families and only
  # the truetype tree is guaranteed present.
  py = pkgs.python3.withPackages (ps: [ ps.fonttools ]);
  checker = pkgs.writeText "glyph-check.py" ''
    import os, re, sys
    from fontTools.ttLib import TTFont

    src, fontdirs = sys.argv[1], sys.argv[2:]
    cmap = {}
    seen_fonts = 0
    for root in fontdirs:
        for dirpath, _, names in os.walk(root):
            for n in names:
                if not n.lower().endswith(".ttf"):
                    continue
                seen_fonts += 1
                for cp, glyph in TTFont(os.path.join(dirpath, n)).getBestCmap().items():
                    cmap.setdefault(cp, glyph)
    if seen_fonts == 0:
        sys.exit("GLYPH FAIL: no .ttf under %s" % (fontdirs,))

    # glyphName = "\U000F044A" // nf-md-record
    pat = re.compile(r'"\\U000([0-9A-Fa-f]{5})"\s*//\s*(nf-[a-z0-9_-]+)')
    found, bad = 0, []
    for dirpath, _, names in os.walk(src):
        for n in names:
            if not n.endswith(".go"):
                continue
            path = os.path.join(dirpath, n)
            with open(path, encoding="utf-8") as fh:
                for lineno, line in enumerate(fh, 1):
                    m = pat.search(line)
                    if not m:
                        continue
                    found += 1
                    cp, want = int(m.group(1), 16), m.group(2).removeprefix("nf-")
                    have = cmap.get(cp)
                    where = "%s:%d" % (os.path.relpath(path, src), lineno)
                    if have is None:
                        bad.append("%s U+%05X is not in the font (tofu), wanted %s"
                                   % (where, cp, want))
                    elif have != want:
                        bad.append("%s U+%05X is %s, not %s"
                                   % (where, cp, have, want))
    print("checked %d glyph pins across %d font files" % (found, seen_fonts))
    if found == 0:
        sys.exit("GLYPH FAIL: no glyph pins matched -- the regex or the source layout moved")
    if bad:
        sys.exit("GLYPH FAIL:\n  " + "\n  ".join(bad))
  '';
in
pkgs.runCommand "khudson-glyph-check" { nativeBuildInputs = [ py ]; } ''
  python3 ${checker} ${src} ${pkgs.lib.escapeShellArgs (map toString fonts)}
  touch $out
''
