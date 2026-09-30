# WASI changes

## Upstream baseline

Rebased onto [mvdan/sh](https://github.com/mvdan/sh) `master` at `cf2204ef` (2026-09-29). The `upstream-main` branch tracks that baseline.

## Changes from upstream

- **wasip1 uses upstream's js/wasm pipes.** Go's `wasip1` target has no `os.Pipe`. Upstream added in-process pipes and a reader-backed standard input for `js/wasm` in `interp/stdin_js.go`; this fork renames it `stdin_wasm.go`, widens its build constraint to `js || wasip1`, and narrows `interp/stdin_os.go` to match. This change is a candidate for upstream.
- **`cmd/shell`** is a non-interactive entry point for WASI. It accepts `-c 'command'` or reads a script from standard input, and does not depend on `golang.org/x/term`.
- **Host command bridge.** `cmd/shell/host_wasi.go` dispatches external commands to the embedding runtime through the imported `env.exec_command` function. Each command is a session the host holds: the shell streams its standard input to the host in chunks, asks the host to run it once, and reads its output back in chunks, so the shell's memory does not grow with the data passing through a pipeline. The file documents the wire format. Every dispatched command's environment carries `PWD` set to the interpreter's directory, whatever the script did to the variable, because the command enters the directory `PWD` names. The rest of the environment is each exported string variable's current value, so one the script unset does not reach the command even when the shell inherited it.
- **Nested shells run in-process.** `bash`, `sh` and the other shell names run as a new interpreter inside the same module rather than through the host, with a bounded nesting depth.
- **A stand-in for `SIGPIPE`.** WASI has no signals. When a pipeline's reader finishes and its writer keeps writing more than a kernel pipe would buffer, the writer stops and its stage exits with status 141, as it would in bash. Upstream's js/wasm pipes do not do this; see `sigpipeWriter` in `interp/runner.go`.
- **`/dev/null` and the standard streams by name.** The runtime's `/dev` holds only an ordinary per-call file named `null`, so the shell's own redirections to `/dev/null`, however the path is spelled, go to an in-process null device instead, and those to `/dev/stdin`, `/dev/stdout` and `/dev/stderr` go to the shell's current standard streams (`openHandler` in `cmd/shell/host_wasi.go`).
- **Glob existence checks use `Stat`.** `expand.Config.Stat` lets globbing check literal path components without reading whole directories, which is slow on large mounted directories.
- **WASI startup and file checks.** The shell starts in `$PWD`, since Go on wasip1 would otherwise start there unchecked, and exits with status 1 without running the script when it cannot enter it, rather than run it in another directory. Without `PWD` it starts in `$HOME`, reporting one it cannot enter, else in `/` when something is mounted there, rather than in Go's first preopen. `checkStat` skips the executable-bit check, since WASI reports no permission bits.

## Build requirement

Go's wasip1 `syscall` package picks the preopen for a path by string prefix rather than by whole path component, so beside a `/lib` mount the shell would resolve `libs/x` to `s/x` inside `/lib`. The shell is built with a `go build -overlay` that corrects the match, which `scripts/go-wasip1-overlay.sh` writes. The overlay fails the build if Go's code changes, and can be removed once Go fixes `preparePath` in `syscall/fs_wasip1.go`.

## Syncing with upstream

```bash
git fetch https://github.com/mvdan/sh.git master:upstream-main
git rebase upstream-main main
```
