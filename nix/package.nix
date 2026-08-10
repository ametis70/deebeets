{ pkgs }:

pkgs.buildGoModule {
  pname = "deeznt";
  version = "0.1.0";
  src = ./..;

  # Run `nix build` once with the placeholder below, then replace with the
  # hash printed in the error message.
  vendorHash = pkgs.lib.fakeHash;
}
