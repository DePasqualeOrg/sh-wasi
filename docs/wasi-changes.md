# WASI changes

## Upstream baseline

Forked from [mvdan/sh](https://github.com/mvdan/sh) at commit `2315483a` (2026-03-26, branch `master`). The `upstream-main` branch tracks this baseline.

## Why this fork exists

Go's `wasip1` target does not implement `os.Pipe` (confirmed as of Go 1.26). Upstream mvdan/sh uses `os.Pipe` in 4 places — pipelines, heredocs, here-strings, and stdin conversion — so it cannot run on WASI without modification.

## Changes from upstream

### `os.Pipe` replaced with `io.Pipe`

The 4 call sites in `interp/runner.go` and `interp/api.go` that use `os.Pipe()` are replaced with `io.Pipe()`, which is pure Go and works on any platform.

| Location | Purpose |
|---|---|
| `interp/runner.go` (pipeline) | Connects stdout of left side to stdin of right side in `a \| b` |
| `interp/runner.go` (hdocReader) | Streams heredoc content to stdin |
| `interp/runner.go` (WordHdoc) | Streams here-string content to stdin |
| `interp/api.go` (stdinFile) | Converts an `io.Reader` to a pipe for the runner — removed entirely, since `Runner.stdin` is now `io.Reader` |

### `Runner.stdin` relaxed from `*os.File` to `io.Reader`

Since `io.Pipe` returns `*io.PipeReader` (not `*os.File`), the `stdin` and `origStdin` fields on `Runner` are changed to `io.Reader`. This also simplifies the `StdIO()` option — it no longer needs to convert non-file readers via a pipe.

`SetReadDeadline` (used by the `read` builtin for timeout support) is now applied conditionally via a type assertion — it works when stdin happens to be an `*os.File`, and is a no-op otherwise.

### `cmd/shell` added

A non-interactive shell entry point designed for WASI environments. Accepts `-c 'command'` or reads from stdin. Does not depend on `golang.org/x/term` (which requires terminal ioctls unavailable in WASI).

## Checking if the fork is still needed

Check whether Go has added `os.Pipe` support for its WASI targets (`wasip1`, or a newer target like `wasip2` if one exists). Compile and run this test program:

```go
package main

import (
	"fmt"
	"os"
)

func main() {
	r, w, err := os.Pipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "os.Pipe: %v\n", err)
		os.Exit(1)
	}
	go func() {
		w.WriteString("ok")
		w.Close()
	}()
	buf := make([]byte, 8)
	n, _ := r.Read(buf)
	fmt.Println(string(buf[:n]))
}
```

```bash
GOOS=wasip1 GOARCH=wasm go build -o test_pipe.wasm test_pipe.go
wasmer run test_pipe.wasm
```

If it prints `ok`, `os.Pipe` is supported and the changes in this fork can be reverted (see below). If a newer WASI target exists (e.g. `wasip2`), try that too — replace `wasip1` in the build command.

If it prints an error like `os.Pipe: pipe: Not implemented on wasip1`, the fork is still needed.

## Reverting the changes

If `os.Pipe` becomes available on WASI:

1. Sync with upstream: `git fetch https://github.com/mvdan/sh.git master:upstream-main && git merge upstream-main`
2. If the merge conflicts on the 4 changed files, take upstream's version — they use `os.Pipe`, which now works.
3. If upstream has refactored significantly and a merge is impractical, the changes can be reverted manually: restore `os.Pipe()` at the 4 call sites listed above, change `Runner.stdin` back to `*os.File`, and restore the `stdinFile()` helper. Use `git diff upstream-main..main -- interp/` to see exactly what differs.
4. Verify: `GOOS=wasip1 GOARCH=wasm go build -o shell.wasm -ldflags="-w -s" ./cmd/shell/` should compile.
5. Test: `wasmer run shell.wasm -- -c 'echo hello | while read x; do echo "got: $x"; done'`

After reverting, the fork's only remaining purpose is `cmd/shell` (the non-interactive WASI entry point).
