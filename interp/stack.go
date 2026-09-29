package interp

// shellStackSize is the goroutine stack size the shell is built with under
// TinyGo's Wasm target, where the build reads it from cmd/shell/stack-size.
// Every goroutine, main included, runs on a stack of this size with no guard
// page. Each goroutine's whole stack is scanned on every collection, so a
// larger stack makes every pipeline stage slower: under Pulley a short
// pipeline took 5 ms with a 64 KiB stack, 12 ms with 128 KiB and 40 ms with
// 512 KiB.
const shellStackSize = 128 << 10
