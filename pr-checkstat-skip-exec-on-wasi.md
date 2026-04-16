# Skip exec-bit check in `checkStat` on WASI

## Summary

`interp/handler.go`'s `checkStat` rejects every regular file in `$PATH` as "permission denied" on WASI preview 1 and preview 2 builds. The gate is `m&0o111 == 0`. On WASI, file permission bits are not real information: wasi-libc's `to_public_stat` never writes the low `0o777`, and mainline Go (`src/os/stat_wasip1.go`) synthesizes a `0o600` default for regular files. The resulting `FileMode` never includes `0o100`, so `command -v`, `type`, and `command -p`/`-P` report every external tool as not found even when the file exists and is runnable via the shell's exec handler.

## Problem

`checkStat` is consumed by `findExecutable` (handler.go ~205), which in turn backs `LookPathDir` (line 239). Callers on the `checkExec=true` path include:

- `command -v <name>` — reports path/not-found (builtin.go:564)
- `type <name>` — prints "is <path>" / "not found" (builtin.go:424)
- `command -p <name>` / `command -P <name>` — same lookup, different output mode (builtin.go:375)
- The default `ExecHandler` when resolving a simple command to run (handler.go:123)

On WASI the reject path fires for all of them because the perm bits never include the executable flag. Reproducing:

```sh
$ sh -c 'command -v jq'
$           # empty output, exit 1
$ sh -c '/bin/jq --version'
jq-1.8.0    # works — the file is there and runnable
```

The file exists, the guest can invoke it (through the interpreter's exec handler), but `command -v` says it doesn't exist.

## Evidence that the perm bits aren't real information on WASI

- **wasi-libc** `libc-bottom-half/cloudlibc/src/libc/sys/stat/stat_impl.h`: `to_public_stat` zero-initializes `st_mode` and ORs in only filetype bits (`S_IFDIR`, `S_IFREG`, etc.). The `0o777` portion is never written.
- **mainline Go** `src/os/stat_wasip1.go` `fillFileStatFromSys` explicitly synthesizes a default because "WASI does not support unix-like permissions, but Go programs are likely to expect the permission bits to not be zero so we set defaults to help avoid breaking applications that are migrating to WASM." It sets `0o700` for directories and `0o600` for regular files. Neither includes the exec bit for files.
- **TinyGo**'s WASI stat path reads `fs.sys.Mode & 0o777` which is always zero on WASI, so it returns `FileMode` with zero permissions unless patched to match mainline Go's `0o600`/`0o700` synthesis. Either way — zero permissions or `0o600` — `m&0o111` is zero.

In all three stacks the result is the same: `0o111 & stat.Mode()` is always zero for regular files on WASI. The existing gate in `checkStat` is therefore a pure false-negative on that platform.

## Change

Extend the existing `runtime.GOOS != "windows"` carve-out to also skip the exec-bit check on `wasip1` and `wasip2`:

```go
if checkExec && runtime.GOOS != "windows" && runtime.GOOS != "wasip1" && runtime.GOOS != "wasip2" && m&0o111 == 0 {
    return "", fmt.Errorf("permission denied")
}
```

Same pattern as windows — defer to the subsequent exec syscall's success/failure when the perm bits aren't meaningful. No change on Linux, macOS, or other Unix targets.

## Why this is the right fix

- The exec-bit check is **unimplementable** on WASI. wasi-libc has no channel to surface the real exec bit, and no Go runtime synthesizes one. Any shell that runs on WASI and looks at `stat.Mode()&0o111` has the same bug.
- Skipping the check is **no looser than the existing Windows path**, which has shipped for years. Windows also can't express Unix exec bits via `os.Stat`, and the windows carve-out explicitly defers to the subsequent `exec`.
- The change is **minimal and targeted**: two tokens added to an existing conditional. No new API, no behavioral change on Unix.

## Validation

- Built mvdan/sh (TinyGo build of `cmd/shell/`) before and after the change against the same WASI shell harness.
- Before: `command -v jq` in the guest shell returns empty (exit 1). `type jq` prints "type: jq: not found".
- After: `command -v jq` prints `/bin/jq` (exit 0). `type jq` prints `jq is /bin/jq`. No regression on `command -v` for shell builtins (which take a different code path and were already working).
- Running `jq` directly continues to work on both builds — the runtime path was never broken, only the lookup gate.

## Risk

The exec-bit check was a pre-flight that rejected non-executable files before the shell tried to run them. Skipping it on WASI means we defer that rejection to the actual exec attempt, which will fail cleanly if the file isn't runnable. For shells, that's the correct semantics — the exec handler is the authoritative check. Same risk profile as the existing Windows carve-out, which has the same rationale.
