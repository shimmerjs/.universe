{
  users.users.shimmerjs = {
    openssh.authorizedKeys.keys = [
      (builtins.readFile ./keys/shimmerjs-key)
      (builtins.readFile ./keys/booninite.keys)
    ];
  };
}
