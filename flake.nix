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
        { pkgs, self', ... }:
        let
          formatters = [
            pkgs.gotools
            pkgs.nixfmt
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
              version = "0.5.1-dev";
              src = ./.;
              vendorHash = "sha256-ZYZ3+s8ptwK/VflUkCgo6pMCtkFR+ppUTq0KYktmKpg=";
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
              pkgs.go
              pkgs.gopls
              pkgs.golangci-lint

              pkgs.just
              pkgs.sqlite
              pkgs.treefmt

              pkgs.protobuf
              pkgs.protoc-gen-go
              pkgs.protoc-gen-go-grpc
            ]
            ++ formatters;
          };
        };
    };
}
