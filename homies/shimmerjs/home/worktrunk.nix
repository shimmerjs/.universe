{ inputs, ... }:
{
  imports = [ inputs.worktrunk.homeModules.default ];

  programs.worktrunk.enable = true;

  # One global layout rule: worktrees live next to the primary checkout,
  # named by branch.
  xdg.configFile."worktrunk/config.toml".text = ''
    worktree-path = "{{ repo_path }}/../{{ branch | sanitize }}"
  '';
}
