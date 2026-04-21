# d2's buildInputs unconditionally include libgbm (mesa), which pulls in
# libdrm — a Linux-only package. This is a nixpkgs bug introduced in
# https://github.com/NixOS/nixpkgs/pull/488723. Gate the Linux-specific
# deps so d2 evaluates cleanly on darwin.
final: prev: {
  d2 = prev.d2.overrideAttrs (old: {
    buildInputs = prev.lib.filter (p: p.pname or "" != "mesa-libgbm") (old.buildInputs or [ ]);
  });
}
