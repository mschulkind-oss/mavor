#!/usr/bin/env bash
# Rewrite a built binary's RUNPATH to the two $ORIGIN entries it needs.
#
# The sherpa-onnx-go-linux module's cgo directive carries
#   -Wl,-rpath,${SRCDIR}/lib/x86_64-unknown-linux-gnu
# so the linker concatenates that absolute build-host path with the
# $ORIGIN:$ORIGIN/../lib rpath the Go linker sets. The result runs, but it
# ships the path of whichever machine built it — the release runner's
# /home/runner/go/pkg/mod/... and, on a Nix host, the store's glibc and gcc
# lib paths as well.
#
# `patchelf --set-rpath` replaces the whole RUNPATH rather than appending to
# it, so this is the one place that removes the leak. The binary and the
# shared objects beside it are the distribution unit; the two entries are
# the same ones the Go linker's -r flag names, in the same order.
#
#   scripts/fix-rpath.sh <binary> [<binary>...]
set -euo pipefail

rpath='$ORIGIN:$ORIGIN/../lib'

if [ "$#" -eq 0 ]; then
    echo "usage: $0 <binary> [<binary>...]" >&2
    exit 1
fi

if ! command -v patchelf >/dev/null 2>&1; then
    echo "fix-rpath: patchelf is not on PATH (add it to the jail's install.nix)" >&2
    exit 1
fi

for bin in "$@"; do
    if [ ! -f "$bin" ]; then
        echo "fix-rpath: $bin does not exist" >&2
        exit 1
    fi
    patchelf --set-rpath "$rpath" "$bin"
    got="$(patchelf --print-rpath "$bin")"
    if [ "$got" != "$rpath" ]; then
        echo "fix-rpath: $bin still has RUNPATH '$got', wanted '$rpath'" >&2
        exit 1
    fi
done
