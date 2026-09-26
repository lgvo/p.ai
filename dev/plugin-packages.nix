{ runCommand, go, src }:
runCommand "p-bundled-plugins-0.1.0" { nativeBuildInputs = [ go ]; } ''
  export GOCACHE="$TMPDIR/go-cache" GOMODCACHE="$TMPDIR/go-modules"
  export GOPROXY=off GOTOOLCHAIN=local
  cd ${src}
  mkdir -p "$out"/{source-git,runtime-incus,environment-nix,tmux-host,file-log,codex-adapter}
  GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -trimpath \
    -o "$out/source-git/git.wasm" ./plugins/bundled/source-git
  GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -trimpath \
    -o "$out/runtime-incus/runtime.wasm" ./plugins/bundled/runtime-incus
  GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -trimpath \
    -o "$out/environment-nix/environment.wasm" ./plugins/bundled/environment-nix
  for name in source-git runtime-incus environment-nix tmux-host file-log codex-adapter; do
    cp "plugins/bundled/$name/plugin.json" "$out/$name/"
  done
  cp plugins/bundled/source-git/p-git-ssh "$out/source-git/"
  cp plugins/bundled/tmux-host/{p-session.target,p-interactive.service,p-attach} "$out/tmux-host/"
  cp plugins/bundled/codex-adapter/p-codex-adapter "$out/codex-adapter/"
''
