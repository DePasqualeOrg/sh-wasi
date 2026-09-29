package interp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

// Chains the parser nests, such as elif branches and flat arithmetic, keep
// their order and results however long they grow.
func TestLongChainsEvaluateInOrder(t *testing.T) {
	t.Parallel()
	var src strings.Builder
	src.WriteString("if false; then echo no\n")
	for i := range 300 {
		fmt.Fprintf(&src, "elif [ %d = 250 ]; then echo elif:%d\n", i, i)
	}
	src.WriteString("else echo else; fi\n")
	src.WriteString("if false; then :; elif false; then :; else echo else:$?; fi\n")
	src.WriteString("if false; then :; elif exit 3; then echo ran; else echo ran; fi\n")
	file, err := syntax.NewParser().Parse(strings.NewReader(src.String()), "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r, err := New(StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), file)
	if want := "elif:250\nelse:1\n"; out.String() != want || err != ExitStatus(3) {
		t.Fatalf("got (%q, %v), want (%q, exit status 3)", out.String(), err, want)
	}

	terms := make([]string, 1000)
	for i := range terms {
		terms[i] = fmt.Sprint(i + 1)
	}
	src.Reset()
	fmt.Fprintf(&src, "echo $((%s)) $((100-10-5-1)) $((2*3+4*5-6/2)) $((x=1, x+=2, x*4))\n", strings.Join(terms, "+"))
	src.WriteString("echo $((1+2+1/0))\n")
	file, err = syntax.NewParser().Parse(strings.NewReader(src.String()), "")
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	r, err = New(StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	r.Run(context.Background(), file)
	if want := "500500 84 23 12\ndivision by zero\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

// The nesting limit on TinyGo's Wasm target is a share of the stack size the
// shell is built with, which the build reads from cmd/shell/stack-size.
func TestShellStackSizeMatchesTheBuild(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../cmd/shell/stack-size")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%dKB", shellStackSize>>10)
	if got := strings.TrimSpace(string(data)); got != want {
		t.Fatalf("cmd/shell/stack-size is %q, but shellStackSize in stack.go is %s; change the two together", got, want)
	}
}
