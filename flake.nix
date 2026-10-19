{
  description = "Local Cosmos SDK validator labs with stock chain binaries";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      date = builtins.substring 0 8 self.lastModifiedDate;
      overlay = final: _prev: {
        forklab = final.callPackage ./package.nix {
          version = "0-unstable-${builtins.substring 0 4 date}-${builtins.substring 4 2 date}-${builtins.substring 6 2 date}";
          rev = self.shortRev or self.dirtyShortRev or "dirty";
        };
      };
    in
    {
      overlays.default = overlay;

      packages = forAllSystems (
        pkgs:
        let
          forklab = (overlay pkgs pkgs).forklab;
        in
        {
          inherit forklab;
          default = forklab;
        }
      );

      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
