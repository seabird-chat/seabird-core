{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";
  };

  outputs =
    inputs@{
      nixpkgs,
      flake-parts,
      ...
    }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = nixpkgs.lib.systems.flakeExposed;
      perSystem =
        {
          pkgs,
          system,
          config,
          lib,
          ...
        }:
        {
          _module.args.pkgs = import nixpkgs {
            inherit system;
            overlays = [
              (final: prev: {
                local = config.packages;
              })
            ];
          };

          formatter = pkgs.treefmt.withConfig {
            runtimeInputs = [
              pkgs.nixfmt-rfc-style
            ];

            settings = {
              on-unmatched = "info";

              formatter.nixfmt = {
                command = "nixfmt";
                includes = [ "*.nix" ];
              };
            };
          };

          devShells.default = pkgs.mkShell {
            packages = [
              pkgs.cargo
              pkgs.rustc
              pkgs.protobuf
              pkgs.rust-analyzer
              pkgs.sqlx-cli
              pkgs.sqlite
            ];

            shellHook = ''
              export RUST_BACKTRACE=1
              export DATABASE_URL="sqlite://$(git rev-parse --show-toplevel)/seabird.db";
            '';
          };

        };
    };
}
