#!/usr/bin/env sh
set -eu

if [ "$#" -eq 0 ]; then
  bin=.cache/bin/p-tui-probe
  mkdir -p "$(dirname "$bin")"
  go build -o "$bin" .
else
  bin=$1
fi
out=evidence/stress
mkdir -p "$out"

variants="table navigator attention command focus lanes outline matrix operations workspace minimal cards attachment governance compare topology actions"
datasets=$($bin --mode stress --list-datasets | awk '{print $1}')

for dataset in $datasets; do
  mkdir -p "$out/$dataset"
  for variant in $variants; do
    for size in 120x35 80x24 60x20 48x16; do
      width=${size%x*}
      height=${size#*x}
      "$bin" --mode stress --dataset "$dataset" --snapshot --variant "$variant" --width "$width" --height "$height" |
        sed 's/[[:space:]]*$//' >"$out/$dataset/$variant-$size.txt"
    done
  done
done
