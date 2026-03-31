## Summary

Use a stat-based check for literal glob path components instead of forcing a full `ReadDir` of each component path.

## Problem

On TinyGo/WASI, `os.ReadDir` can be much more expensive than `os.Stat` on large mounted directories because the `ReadDir` path may fall back to metadata lookups for each entry. In our shell runtime this showed up as a hang for commands like:

```sh
echo /mnt/<uuid>/*.txt
```

The expensive part was not the final `*.txt` expansion. It was the literal path-component check in `expand.Config.glob`, which called `ReadDir2("/mnt")` and `ReadDir2("/mnt/<uuid>")` just to confirm those components existed and were directories.

That is unnecessarily costly when a simple stat is enough.

## Change

- Add `expand.Config.Stat` for existence and directory checks during globbing
- Use `Stat` for non-meta path components in `glob`
- Use `Stat` for symlink-to-directory checks in `globDir`
- Wire the interpreter's existing `statHandler` into `expand.Config.Stat`
- Keep the existing `ReadDir2` fallback when no stat hook is provided

## Why this is preferable

- Avoids a full directory enumeration for literal components like `/mnt`
- Preserves the existing `ReadDir2`-based matching for the actual globbed segment
- Reuses the interpreter's existing filesystem abstraction instead of hardcoding `os.Stat`
- Keeps the change local to glob existence checks rather than requiring TinyGo-specific behavior

## Validation

- Built the TinyGo shell successfully after the change
- Reproduced the previous Wasmer + TinyGo hang with the old shell
- Verified that the patched TinyGo shell makes the same regression test pass:

`shellGlobHostTmpMountedElsewhere_debug()` passed in about `0.18s`

## Follow-up

TinyGo's `os.ReadDir` behavior on WASI may still be worth investigating separately, but this shell-side change removes an avoidable pathological call pattern and fixes the observed regression without requiring runtime-specific logic.
