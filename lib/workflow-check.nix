# Lints clod workflow scripts: JS syntax + every agentType resolves to a wired
# agent. Used by mkchecks.nix to produce a `clod-workflows-<host>` flake check.
{ pkgs, lib, workflowsDir, validAgents }:
pkgs.runCommand "clod-workflow-lint"
  {
    nativeBuildInputs = [ pkgs.nodejs pkgs.python3 ];
    inherit workflowsDir;
    validAgents = lib.concatStringsSep " " validAgents;
  }
  ''
    python3 ${./workflow-lint.py}
    touch $out
  ''
