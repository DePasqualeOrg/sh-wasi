// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build js || wasip1

package interp

import (
	"context"
	"io"
	"time"
)

// js/wasm and wasip1 have no OS pipes and no subprocesses, so pipes are in-process
// [io.Pipe]s and any reader can be used as stdin directly. Read
// deadlines are not supported: SetReadDeadline is a no-op, so a
// blocked `read` builtin unblocks on the next write rather than on
// context cancellation.

// stdinFile is the runner's standard input; elsewhere it is an
// [*os.File], but on js/wasm and wasip1 any reader satisfying this interface works.
type stdinFile interface {
	io.ReadCloser
	SetReadDeadline(t time.Time) error
}

// jsReader adapts a plain reader into a stdinFile.
type jsReader struct {
	io.Reader
}

func (r jsReader) Close() error {
	if c, ok := r.Reader.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func (jsReader) SetReadDeadline(t time.Time) error { return nil }

// newPipe returns an in-process pipe.
func newPipe() (stdinFile, *io.PipeWriter, error) {
	pr, pw := io.Pipe()
	return jsReader{pr}, pw, nil
}

// newStdinFile converts a reader into the runner's stdin. Without
// subprocesses there is no need to copy through a pipe.
func newStdinFile(r io.Reader) (stdinFile, error) {
	if r == nil {
		return nil, nil
	}
	return jsReader{r}, nil
}

// stdinTerminal always reports false, as js/wasm and wasip1 have no terminals.
func stdinTerminal(stdin stdinFile) (int, bool) { return -1, false }

// stageOutput is where a pipeline stage writes. An in-process pipe has no
// SIGPIPE, so a [sigpipeWriter] stands in for it; isBroken reports whether
// the stage's own writes broke the pipe.
func stageOutput(pw *io.PipeWriter, stop context.CancelFunc) (out io.Writer, isBroken func() bool) {
	w := &sigpipeWriter{w: pw, stop: stop}
	return w, w.isBroken
}
