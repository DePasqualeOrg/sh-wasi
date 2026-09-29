//go:build !tinygo.wasm

package interp

// stackExhausted is always false where goroutine stacks grow.
func (r *Runner) stackExhausted() bool { return false }
