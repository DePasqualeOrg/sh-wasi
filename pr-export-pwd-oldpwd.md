# Export PWD and OLDPWD

## Summary

`PWD` and `OLDPWD` are set via `setVarString`, which creates variables with `Exported: false`. POSIX requires both to be exported so child processes inherit them.

> "The following variables shall be set by the implementation..." — POSIX.1-2017, Shell & Utilities, Section 2.5.3

Most other shells (bash, zsh, dash, busybox ash) export PWD by default. The current behavior means `PWD` is visible inside the shell (`echo $PWD` works) but not passed to child processes via the environment.

## Changes

- `api.go`: Use `setVar` with `Exported: true` when initializing `PWD` in `Reset()`.
- `builtin.go`: Use `setVar` with `Exported: true` when updating `PWD` and `OLDPWD` in `changeDir()`.

## Test

```sh
# Before: PWD is not in the child's environment
sh -c 'cd /tmp && env' | grep PWD
# (no output)

# After: PWD is exported
sh -c 'cd /tmp && env' | grep PWD
# PWD=/tmp
# OLDPWD=/
```
