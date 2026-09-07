#!/usr/bin/env sh
set -eu

if [ "$#" -eq 0 ]; then
  bin=.cache/bin/p-tui-probe
  mkdir -p "$(dirname "$bin")"
  go build -o "$bin" .
else
  bin=$1
fi

out=evidence/stress/churn-sequence
variants="table navigator attention command focus lanes outline matrix operations workspace minimal cards attachment governance compare topology actions"

for step in 0 1 2 3 4; do
  mkdir -p "$out/step-$step"
  for variant in $variants; do
    for size in 120x35 80x24 60x20 48x16; do
      width=${size%x*}
      height=${size#*x}
      "$bin" --mode stress --dataset churn --stress-step "$step" --snapshot --variant "$variant" --width "$width" --height "$height" |
        sed 's/[[:space:]]*$//' >"$out/step-$step/$variant-$size.txt"
    done
  done
done
