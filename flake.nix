{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    devshell.url = "github:numtide/devshell";
    flake-utils.url = "github:numtide/flake-utils";

    flake-compat = {
      url = "github:edolstra/flake-compat";
      flake = false;
    };

    rust-overlay = {
      url = "github:oxalica/rust-overlay";
      inputs = {
        nixpkgs.follows = "nixpkgs";
        flake-utils.follows = "flake-utils";
      };
    };
  };

  outputs = { self, flake-utils, rust-overlay, devshell, nixpkgs, ... }:
    flake-utils.lib.eachDefaultSystem (system: {
      devShell =
        let
          pkgs = import nixpkgs {
            inherit system;

            overlays = [
              devshell.overlays.default
              (import rust-overlay)
            ];
          };
        in
        pkgs.devshell.mkShell {
          devshell.motd = "";

          devshell.packages = [
            (pkgs.rust-bin.stable."1.71.1".default.override {
              extensions = ["rust-src"];
            })
            pkgs.protobuf
            pkgs.rust-analyzer
            pkgs.sqlx-cli
            pkgs.sqlite
          ];

          env = [
            {
              name = "RUST_BACKTRACE";
              value = "1";
            }
            {
              # We set the DATABASE_URL in a shell hook so we can reference the
              # project directory, not a directory in the nix store.
              name = "DATABASE_URL";
              eval = "sqlite://$(git rev-parse --show-toplevel)/seabird.db";
            }
          ];
        };
    });
}
