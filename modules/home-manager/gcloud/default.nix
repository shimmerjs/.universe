{ pkgs, ... }:
{
  home.packages = with pkgs;
    [
      # Get extra components for additional clout in the Google Clout SDK.
      (google-cloud-sdk.withExtraComponents
        [ google-cloud-sdk.components.gke-gcloud-auth-plugin ])
    ];

  programs.zsh = {
    sessionVariables = {
      USE_GKE_GCLOUD_AUTH_PLUGIN = "True";
    };
  };
}
