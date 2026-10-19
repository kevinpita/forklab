{
  lib,
  stdenv,
  buildGo127Module,
  installShellFiles,
  version,
  rev,
}:

buildGo127Module {
  pname = "forklab";
  inherit version;

  src = lib.fileset.toSource {
    root = ./.;
    fileset = lib.fileset.unions [
      ./go.mod
      ./go.sum
      ./skill.go
      ./cmd
      ./internal
      ./docs
      ./.agents/skills/forklab
    ];
  };

  vendorHash = "sha256-dKM7W8uERzlZ9oEtDCUISvs6Nzcm2yx7frR7CVrA4ug=";

  subPackages = [ "cmd/forklab" ];

  env.CGO_ENABLED = 0;

  ldflags = [
    "-s"
    "-w"
    "-X github.com/kevinpita/forklab/internal/cli.version=${rev}"
  ];

  nativeBuildInputs = [ installShellFiles ];

  # The skill links to ../../../docs, so keep the release archive layout.
  postInstall = ''
    share="$out/share/forklab"
    mkdir -p "$share/.agents/skills"
    cp -r docs "$share/docs"
    cp -r .agents/skills/forklab "$share/.agents/skills/forklab"
  ''
  + lib.optionalString (stdenv.buildPlatform.canExecute stdenv.hostPlatform) ''
    installShellCompletion --cmd forklab \
      --bash <($out/bin/forklab completion bash) \
      --fish <($out/bin/forklab completion fish) \
      --zsh <($out/bin/forklab completion zsh)
  '';

  meta = {
    description = "Local Cosmos SDK validator labs with stock chain binaries";
    homepage = "https://github.com/kevinpita/forklab";
    license = lib.licenses.mit;
    mainProgram = "forklab";
  };
}
