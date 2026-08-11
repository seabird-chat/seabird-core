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
          formatter = pkgs.treefmt.withConfig {
            runtimeInputs = [
              pkgs.nixfmt-rfc-style
              pkgs.rustfmt
            ];

            settings = {
              on-unmatched = "info";

              formatter.nixfmt = {
                command = "nixfmt";
                includes = [ "*.nix" ];
              };

              formatter.rustfmt = {
                command = "rustfmt";
                includes = [ "*.rs" ];
              };
            };
          };

          packages.default = pkgs.rustPlatform.buildRustPackage {
            pname = "seabird-core";
            version = (builtins.fromTOML (builtins.readFile ./Cargo.toml)).package.version;
            src = ./.;
            cargoLock.lockFile = ./Cargo.lock;
            nativeBuildInputs = [ pkgs.protobuf ];

            # Ensure we use sqlx in offline mode so it doesn't try to talk to
            # a live database.
            SQLX_OFFLINE = true;
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
