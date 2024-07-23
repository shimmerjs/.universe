# Personal macmini
{
  system = "aarch64-darwin";
  user = "shimmerjs";

  homie = import ../homies/shimmerjs;

  systemConfig = { user, ... }: {
    users.users.${user}.openssh.authorizedKeys = {
      keyFiles = [
        ../homies/shimmerjs/shimmerjs.pub
      ];
    };
  };
}
