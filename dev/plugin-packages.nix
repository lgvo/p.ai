{ runCommand, src }:
runCommand "p-bundled-plugins-0.1.0" { } ''
  cd ${src}
  catalog="$out/plugins"
  notices="$out/licenses"
  mkdir -p "$catalog"/{file-log,codex-adapter}
  for name in file-log codex-adapter; do
    cp "plugins/bundled/$name/plugin.json" "$catalog/$name/"
    mkdir -p "$notices/$name"
    cp LICENSE "$notices/$name/"
  done
  cp plugins/bundled/codex-adapter/p-codex-adapter "$catalog/codex-adapter/"
''
