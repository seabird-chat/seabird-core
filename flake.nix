{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";

    seabird-protos = {
      url = "git+file:proto";
      flake = false;
    };
  };

  outputs =
    inputs@{
      flake-parts,
      nixpkgs,
      seabird-protos,
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
            runtimeInputs = [ pkgs.nixfmt-rfc-style ];

            settings = {
              # Log level for files treefmt won't format
              on-unmatched = "info";

              # Configure nixfmt for .nix files
              formatter.nixfmt = {
                command = "nixfmt";
                includes = [ "*.nix" ];
              };
            };
          };

          packages = {
            default = config.packages.seabird-core;

            seabird-core = pkgs.rustPlatform.buildRustPackage {
              pname = "seabird-core";
              version = "0.3.3-dev";

              src = ./.;

              cargoHash = "sha256-M/jncfud4U4n4UBnXGcW1uMBgxBMG9WZefSFEsDSKso=";

              env = {
                SEABIRD_PROTO_PATH = "${seabird-protos}";
              };

              nativeBuildInputs = [
                pkgs.protobuf
              ];
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
