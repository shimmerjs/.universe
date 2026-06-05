# home.file entries for the deployable contents of this directory: every workflow
# script (*.js) plus the stage-prompt partials under partials/ (the *.md that the
# synthesis stages read at ~/.claude/workflows/partials/). NOT deployed: the
# authoring CLAUDE.md or this default.nix. Dropping a new .js here, or a new
# partials/*.md, deploys it with no edits anywhere. Imported by ../default.nix.
{ lib }:
let
  js = lib.mapAttrs'
    (name: _: lib.nameValuePair ".claude/workflows/${name}" { source = ./${name}; })
    (lib.filterAttrs (name: type: type == "regular" && lib.hasSuffix ".js" name)
      (builtins.readDir ./.));
  partials = lib.mapAttrs'
    (name: _: lib.nameValuePair ".claude/workflows/partials/${name}" { source = ./partials/${name}; })
    (lib.filterAttrs (name: type: type == "regular" && lib.hasSuffix ".md" name)
      (builtins.readDir ./partials));
in
js // partials
