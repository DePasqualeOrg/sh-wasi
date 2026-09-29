package interp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

func TestSigpipeWriterBuffersAPipesWorth(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	pr.Close()
	stopped := false
	w := &sigpipeWriter{w: pw, stop: func() { stopped = true }}

	if n, err := w.Write(make([]byte, pipeCapacity)); n != pipeCapacity || err != nil {
		t.Fatalf("a pipe's worth of unread output: got (%d, %v), want (%d, nil)", n, err, pipeCapacity)
	}
	if stopped || w.isBroken() {
		t.Fatal("a pipe's worth of unread output broke the pipe")
	}
	if _, err := w.WriteExternal([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("external write past the buffer: got %v, want io.ErrClosedPipe", err)
	}
	if stopped {
		t.Fatal("an external command's broken pipe stopped the stage")
	}
	if _, err := w.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("shell write past the buffer: got %v, want io.ErrClosedPipe", err)
	}
	if !stopped || !w.isBroken() {
		t.Fatal("the shell's own broken pipe did not stop the stage")
	}
}

func TestPipelineStandsInForSigpipe(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "js" && runtime.GOOS != "wasip1" {
		t.Skip("pipes here are OS pipes, which deliver SIGPIPE themselves")
	}
	src := `set -o pipefail
printf 'a\nb\n' | { read -r l; }; echo "small:$?"
while true; do echo y; done | { read -r l; }; echo "loop:$?"
set -e; true && false && true; echo "chain:$?"`
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r, err := New(StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatalf("Run: %v; output %q", err, out.String())
	}
	if want := "small:0\nloop:141\nchain:1\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}
