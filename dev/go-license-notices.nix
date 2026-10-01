{ runCommand, go }:
# go-licenses excludes the standard library. Retain its notices from the exact
# source archive used by the pinned compiler, including vendored Go packages.
runCommand "p-go-${go.version}-notices" { } ''
  tar -xf ${go.src} go/LICENSE go/PATENTS go/src/vendor
  mkdir -p "$out"
  cp go/LICENSE go/PATENTS "$out/"
  printf '%s\n' '${go.version}' > "$out/VERSION"
  while IFS= read -r -d $'\0' notice; do
    relative="''${notice#go/src/}"
    mkdir -p "$out/$(dirname "$relative")"
    cp "$notice" "$out/$relative"
  done < <(find go/src/vendor -type f \( -name LICENSE -o -name NOTICE -o -name COPYING \) -print0)
''
