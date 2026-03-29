//go:build !wasip1

package main

import "mvdan.cc/sh/v3/interp"

// On non-WASI platforms, fall through to the default os/exec handler.
func hostExecHandler(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return next
}
