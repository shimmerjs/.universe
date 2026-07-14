# plannotator: plan-mode review in the browser. The plugin's hooks fire on
# EnterPlanMode (context improver, 5s) and on the ExitPlanMode permission
# request, where `plannotator` serves the annotation UI and blocks until
# the review lands (timeout 4 days) -- approving a plan becomes a markup
# session instead of a y/n.
#
# Upstream installs via `curl | bash` dropping a binary in ~/.local/bin
# plus a marketplace add. Neither here: the plugin dir sideloads from the
# tag-pinned flake input (worktrunk precedent), and the binary is the
# upstream release artifact (bun-compiled, self-contained, no runtime
# deps) fetched by its published sha256. The hooks exec bare `plannotator`
# from PATH, so home.packages carries it; the plugin dir stays byte-stock
# so upstream bumps are input bumps. Version and input tag move TOGETHER
# (see flake.nix).
{ pkgs, inputs, ... }:
let
  plannotator = pkgs.stdenvNoCC.mkDerivation {
    pname = "plannotator";
    version = "0.23.1";
    src = pkgs.fetchurl {
      url = "https://github.com/backnotprop/plannotator/releases/download/v0.23.1/plannotator-darwin-arm64";
      hash = "sha256-2BzH2MrM/EaJTL4WQblmMXcNsvOWtJpY4q1f7icVBfE=";
    };
    dontUnpack = true;
    installPhase = ''
      install -m755 -D $src $out/bin/plannotator
    '';
  };
in
{
  programs.claude-code = {
    plugins = [
      "${inputs.plannotator}/apps/hook"
    ];
    # the plan-mode hooks above almost never fire here (transcript history:
    # 1 of 224 sessions used plan mode) -- planning lives in design/DEVPLAN
    # markdown. The annotate skill bridges that culture to the same review
    # UI: `plannotator annotate <doc> --gate --json`, backgrounded, looped
    # until the gate approves.
    skills.annotate = ./annotate/SKILL.md;
  };
  home.packages = [ plannotator ];
}
