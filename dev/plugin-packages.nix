{ runCommand, go, go-licenses, callPackage, src }:
let
  goNotices = callPackage ./go-license-notices.nix { };
in
runCommand "p-bundled-plugins-0.1.0" { nativeBuildInputs = [ go go-licenses ]; } ''
  export GOCACHE="$TMPDIR/go-cache" GOMODCACHE="$TMPDIR/go-modules"
  export GOPROXY=off GOTOOLCHAIN=local
  cd ${src}
  catalog="$out/plugins"
  notices="$out/licenses"
  mkdir -p "$catalog"/{source-git,runtime-incus,environment-nix,tmux-host,file-log,codex-adapter}
  GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -trimpath \
    -o "$catalog/source-git/git.wasm" ./plugins/bundled/source-git
  GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -trimpath \
    -o "$catalog/runtime-incus/runtime.wasm" ./plugins/bundled/runtime-incus
  GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -trimpath \
    -o "$catalog/environment-nix/environment.wasm" ./plugins/bundled/environment-nix
  for name in source-git runtime-incus environment-nix tmux-host file-log codex-adapter; do
    cp "plugins/bundled/$name/plugin.json" "$catalog/$name/"
    mkdir -p "$notices/$name"
    cp LICENSE "$notices/$name/"
  done
  for name in source-git runtime-incus environment-nix; do
    GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go-licenses check \
      --allowed_licenses=Apache-2.0,MIT,BSD-2-Clause,BSD-3-Clause,ISC "./plugins/bundled/$name"
    GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go-licenses save \
      --save_path="$notices/$name/modules" "./plugins/bundled/$name"
    cp -R ${goNotices} "$notices/$name/go"
  done
  cp plugins/bundled/source-git/p-git-ssh "$catalog/source-git/"
  cp plugins/bundled/tmux-host/{p-session.target,p-interactive.service,p-attach} "$catalog/tmux-host/"
  cp plugins/bundled/codex-adapter/p-codex-adapter "$catalog/codex-adapter/"
''
