#!/usr/bin/env bash
# Locate the sherpa-onnx shared objects the cgo build links against.
#
# mavor is a cgo program: `mavor` on its own is not a runnable artifact, it
# needs libsherpa-onnx-c-api.so and libonnxruntime.so beside it (or in
# ../lib — see the $ORIGIN RUNPATH the build sets). Those two are vendored
# inside the Go module cache rather than committed here, so every place that
# ships mavor has to go and find them. This is that one place: the Justfile's
# `build` and `install`, and the goreleaser `before` hook, all call it.
#
#   scripts/sherpa-libs.sh              print the absolute path of each, one
#                                       per line
#   scripts/sherpa-libs.sh <dir>        copy them into <dir> instead
#
# libsherpa-onnx-cxx-api.so ships in the same directory and is deliberately
# not listed: nothing in mavor links it, and it would be 260 KB of nothing in
# every archive.
set -euo pipefail

module=github.com/k2-fsa/sherpa-onnx-go-linux
libs=(libonnxruntime.so libsherpa-onnx-c-api.so)

# The module directory is asked for with `-f`, whose argument is a Go
# template. Keeping that template in a shell script rather than in
# .goreleaser.yaml matters: goreleaser renders that file as a Go template
# first, so a `{{ }}` written there is consumed before `go list` ever sees it.
dir="$(go list -m -f '{{.Dir}}' "$module")/lib/x86_64-unknown-linux-gnu"

for lib in "${libs[@]}"; do
    if [ ! -f "$dir/$lib" ]; then
        echo "sherpa-libs: $dir/$lib is missing — run 'go mod download'" >&2
        exit 1
    fi
done

if [ "$#" -eq 0 ]; then
    for lib in "${libs[@]}"; do
        echo "$dir/$lib"
    done
    exit 0
fi

dest="$1"
mkdir -p "$dest"
for lib in "${libs[@]}"; do
    # The module cache is read-only, so the copies need their mode set
    # explicitly rather than inherited.
    install -m 0644 "$dir/$lib" "$dest/$lib"
done
