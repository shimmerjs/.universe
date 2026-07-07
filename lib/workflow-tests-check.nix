# Runs the workflow fixture unit tests: workflow-tests.mjs extracts the shared
# parser and the body-level pure helpers from each aw-*.js and asserts them
# against the JSON fixtures in <workflowsDir>/testdata. Used by mkchecks.nix to
# produce a `clod-workflow-tests-<host>` flake check.
{ pkgs, workflowsDir }:
pkgs.runCommand "clod-workflow-tests"
  {
    nativeBuildInputs = [ pkgs.nodejs ];
    inherit workflowsDir;
    fixturesDir = workflowsDir + "/testdata";
  }
  ''
    node ${./workflow-tests.mjs}
    touch $out
  ''
