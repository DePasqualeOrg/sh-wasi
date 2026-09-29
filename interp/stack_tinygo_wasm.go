//go:build tinygo.wasm

package interp

import _ "unsafe" // for go:linkname

// stackLimit is how much of its stack a goroutine may use before statements
// stop nesting. The rest is for a goroutine that blocks: TinyGo's asyncify
// scheduler saves it into the bottom of its own stack, and running out of
// room there traps, while running the stack itself past its end would
// corrupt the memory below it.
const stackLimit = shellStackSize * 2 / 3

// stackExhausted reports whether this goroutine has used its share of its
// stack. A runner that starts a goroutine clears stackBase, so the first
// statement on the new stack records where it begins.
func (r *Runner) stackExhausted() bool {
	sp := getCurrentStackPointer()
	if r.stackBase == 0 {
		r.stackBase = sp
		return false
	}
	return r.stackBase > sp && r.stackBase-sp > stackLimit
}

// getCurrentStackPointer reads the stack pointer, from the assembly helper
// TinyGo's runtime uses for the same purpose.
//
//go:linkname getCurrentStackPointer tinygo_getCurrentStackPointer
func getCurrentStackPointer() uintptr
