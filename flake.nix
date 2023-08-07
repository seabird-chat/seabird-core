{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    rust-overlay = {
      url = "github:oxalica/rust-overlay";
      inputs = {
        nixpkgs.follows = "nixpkgs";
        flake-utils.follows = "flake-utils";
      };
    };
  };

  outputs = { self, nixpkgs, rust-overlay, flake-utils }:
    flake-utils.lib.eachDefaultSystem
      (system:
        let
          pkgs = import nixpkgs {
            inherit system;
            overlays = [ (import rust-overlay) ];
          };
        in
        {
          devShells.default = pkgs.mkShell {
            nativeBuildInputs = [
              pkgs.protobuf
              (pkgs.rust-bin.stable."1.71.1".default.override {
                extensions = ["rust-src"];
              })
              pkgs.rust-analyzer
              pkgs.sqlx-cli
              pkgs.sqlite
            ];

            RUST_BACKTRACE = 1;

            # We set the DATABASE_URL in a shell hook so we can reference the
            # project directory, not a directory in the nix store.
            shellHook = ''
              export DATABASE_URL="sqlite://$(git rev-parse --show-toplevel)/seabird.db"
            '';
          };
        }
      );
}
