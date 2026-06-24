# Derives ~/.claude/workflows/cheatsheet.json from the workflow metas at build
# time (see cheatsheet-gen.mjs). Imported by ../default.nix as a home.file source.
# Mirrors lib/workflow-check.nix's runCommand+nodejs pattern. The generator exits
# non-zero on an unextractable meta or a missing flags block, so a malformed
# workflow fails the rebuild loudly instead of shipping a stale/partial sheet.
# Nix only sees git-tracked files: cheatsheet-gen.mjs and the aw-*.js must be
# `git add`ed for ${./.} to include them.
{ pkgs }:
pkgs.runCommand "clod-cheatsheet-json"
  { nativeBuildInputs = [ pkgs.nodejs ]; }
  ''
    node ${./cheatsheet-gen.mjs} ${./.} > $out
  ''
