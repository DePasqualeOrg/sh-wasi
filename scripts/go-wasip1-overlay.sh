#!/bin/bash
# Writes a `go build -overlay` file that corrects how Go's wasip1 syscall
# package picks a preopen for a path, and prints the overlay's path. Every
# build of the shell for WASI needs it; see docs/wasi-changes.md.
#
# Go takes the longest preopen that is a string prefix of the path without
# requiring the match to end at a path component, so with a preopen at /lib,
# /libs/x resolves to s/x inside /lib, and with / and /dev preopened,
# `echo x > devnotes.md` in / writes /dev/notes.md, where a tool built on
# wasi-libc, which matches whole components, writes /devnotes.md. The overlay
# leaves the toolchain untouched, and fails loudly if the code it corrects has
# changed.
#
# Usage: go-wasip1-overlay.sh <output-dir>
set -euo pipefail

out_dir="$1"
# Ask from the module, so toolchain selection names the Go that builds it.
src="$(cd "$(dirname "$0")/.." && go env GOROOT)/src/syscall/fs_wasip1.go"
old='if len(p.name) > len(dirName) && stringslite.HasPrefix(path, p.name) {'
new='if len(p.name) > len(dirName) && stringslite.HasPrefix(path, p.name) && (len(path) == len(p.name) || p.name[len(p.name)-1] == '"'/'"' || path[len(p.name)] == '"'/'"') {'

content="$(<"$src")"
if [[ "$content" != *"$old"* ]]; then
    echo "Error: $src no longer contains the preopen match this overlay corrects." >&2
    echo "  Check whether Go fixed it upstream, then update or remove scripts/go-wasip1-overlay.sh." >&2
    exit 1
fi

mkdir -p "$out_dir"
# Split around the match rather than substituting: bash 3.2 keeps the quotes
# of a quoted replacement, and bash 5.2 expands `&` in an unquoted one.
printf '%s%s%s\n' "${content%%"$old"*}" "$new" "${content#*"$old"}" > "$out_dir/fs_wasip1.go"
printf '{"Replace":{"%s":"%s"}}\n' "$src" "$out_dir/fs_wasip1.go" > "$out_dir/overlay.json"
echo "$out_dir/overlay.json"
