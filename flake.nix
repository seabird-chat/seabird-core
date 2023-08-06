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
            ];

            RUST_BACKTRACE = 1;
          };
        }
      );
}
