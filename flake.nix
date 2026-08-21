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
          ...
        }:
        let
          version = (builtins.fromTOML (builtins.readFile ./Cargo.toml)).package.version;
        in
        {
          formatter = pkgs.treefmt.withConfig {
            runtimeInputs = [
              pkgs.nixfmt-rfc-style
              pkgs.rustfmt
              pkgs.gotools
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

              formatter.goimports = {
                command = "goimports";
                options = [ "-w" ];
                includes = [ "*.go" ];
              };
            };
          };

          packages.default = pkgs.rustPlatform.buildRustPackage {
            pname = "seabird-core";
            inherit version;
            src = ./.;
            cargoLock.lockFile = ./Cargo.lock;
            nativeBuildInputs = [ pkgs.protobuf ];

            SEABIRD_PROTO_PATH = "${proto}";

            # Ensure we use sqlx in offline mode so it doesn't try to talk to
            # a live database.
            SQLX_OFFLINE = true;
          };

          # The Go port, which will take over as the default package once it's
          # been through staging. It gets the protos from seabird-go rather than
          # from the proto input.
          #
          # subPackages is deliberately unset: the only main package is
          # cmd/seabird-core, and leaving it out means the check phase runs the
          # tests under internal/ too.
          packages.seabird-core-go = pkgs.buildGoModule {
            pname = "seabird-core";
            inherit version;
            src = ./.;

            vendorHash = "sha256-KLh1AC53c8qwQHMUKrftYe93P8azV4/OLW1DRZ/90lI=";

            ldflags = [
              "-s"
              "-w"
            ];
          };

          devShells.default = pkgs.mkShell {
            packages = [
              pkgs.cargo
              pkgs.rustc
              pkgs.protobuf
              pkgs.rust-analyzer
              pkgs.sqlx-cli
              pkgs.sqlite
              pkgs.go
              pkgs.gopls
              pkgs.gotools
            ];

            shellHook = ''
              export RUST_BACKTRACE=1
              export DATABASE_URL="sqlite://$(git rev-parse --show-toplevel)/seabird.db";
              export SEABIRD_PROTO_PATH="${proto}";
            '';
          };

        };
    };
}
