# The bundled packages' files from the source tree, and their notices. The
# file-log package's entry, p-event-file, is built with P, which adds it.
{ runCommand, src }:
runCommand "p-bundled-plugins-0.2.0" { } ''
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
