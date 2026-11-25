{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-parts.url = "github:hercules-ci/flake-parts";

    proto = {
      url = "github:seabird-chat/proto";
      flake = false;
    };
  };

  outputs =
    inputs@{
      nixpkgs,
      flake-parts,
      proto,
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
          self',
          ...
        }:
        let
          formatters = [
            pkgs.gotools
            pkgs.nixfmt-rfc-style
            pkgs.rustfmt
          ];
        in
        {
          formatter = pkgs.treefmt.withConfig {
            runtimeInputs = formatters;
            configFile = ./treefmt.toml;
          };

          packages = {
            default = self'.packages.seabird-core;

            seabird-core = pkgs.buildGoModule rec {
              pname = "seabird-core";
              version = "0.4.0-dev";
              src = ./.;
              vendorHash = "sha256-TYgEPcGyqfep1hv5hAAiBtRcGeuszjRqVcd5z1uGyBc=";
              subPackages = [ "cmd/seabird-core" ];

              ldflags = [
                "-s"
                "-w"
                "-X main.Version=v${version}"
              ];

              env.CGO_ENABLED = 0;
            };
          };

          devShells.default = pkgs.mkShell {
            packages = [
              pkgs.cargo
              pkgs.rustc
              pkgs.protobuf
              pkgs.rust-analyzer
              pkgs.sqlx-cli

              pkgs.go
              pkgs.gopls
              pkgs.golangci-lint

              pkgs.just
              pkgs.sqlite
              pkgs.treefmt
            ]
            ++ formatters;

            shellHook = ''
              export RUST_BACKTRACE=1
              export DATABASE_URL="sqlite://$(git rev-parse --show-toplevel)/seabird.db";
              export SEABIRD_PROTO_PATH="${proto}";
            '';
          };
        };
    };
}
