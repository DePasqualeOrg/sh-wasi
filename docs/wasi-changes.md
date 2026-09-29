# WASI changes

## Upstream baseline

Rebased onto [mvdan/sh](https://github.com/mvdan/sh) `master` at `cf2204ef` (2026-09-29). The `upstream-main` branch tracks that baseline.

## Changes from upstream

- **wasip1 uses upstream's js/wasm pipes.** Go's `wasip1` target has no `os.Pipe`. Upstream added in-process pipes and a reader-backed standard input for `js/wasm` in `interp/stdin_js.go`; this fork renames it `stdin_wasm.go`, widens its build constraint to `js || wasip1`, and narrows `interp/stdin_os.go` to match. This change is a candidate for upstream.
- **`cmd/shell`** is a non-interactive entry point for WASI. It accepts `-c 'command'` or reads a script from standard input, and does not depend on `golang.org/x/term`.
- **Host command bridge.** `cmd/shell/host_wasi.go` dispatches external commands to the embedding runtime through the imported `env.exec_command` function.
- **Glob existence checks use `Stat`.** `expand.Config.Stat` lets globbing check literal path components without reading whole directories, which is slow on large mounted directories.
- **WASI startup and file checks.** The shell starts in `$HOME`, and `checkStat` skips the executable-bit check, since WASI reports no permission bits.

## Syncing with upstream

```bash
git fetch https://github.com/mvdan/sh.git master:upstream-main
git rebase upstream-main main
```
