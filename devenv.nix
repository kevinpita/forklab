{ pkgs, ... }:

let
  buildGoModule = pkgs.buildGo127Module;
in
{
  languages.go = {
    enable = true;
    package = pkgs.go_1_27;
  };

  packages = [
    (pkgs.gofumpt.override { inherit buildGoModule; })
    pkgs.golangci-lint # already built with go 1.27 upstream
    (pkgs.gotools.override { inherit buildGoModule; })
    pkgs.lz4
    pkgs.jq
  ];

  scripts.lint.exec = "golangci-lint run ./...";
  scripts.fmt.exec = "gofumpt -l -w .";
}
