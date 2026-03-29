// shell is a non-interactive shell for WASI environments.
// It parses and executes shell commands, dispatching external
// commands through a pluggable ExecHandler.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

func main() {
	err := run()
	var es interp.ExitStatus
	if errors.As(err, &es) {
		os.Exit(int(es))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	// Parse -c flag manually since flag package works but we want
	// to keep the binary minimal.
	var command string
	var scriptArgs []string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			if i+1 >= len(args) {
				return fmt.Errorf("shell: -c requires an argument")
			}
			command = args[i+1]
			scriptArgs = args[i+2:]
			break
		}
	}

	r, err := interp.New(
		interp.StdIO(os.Stdin, os.Stdout, os.Stderr),
		interp.Params(scriptArgs...),
	)
	if err != nil {
		return err
	}

	if command != "" {
		return execute(r, strings.NewReader(command), "")
	}
	// No -c flag: read from stdin
	return execute(r, os.Stdin, "")
}

func execute(r *interp.Runner, reader io.Reader, name string) error {
	p := syntax.NewParser()
	f, err := p.Parse(reader, name)
	if err != nil {
		return err
	}
	return r.Run(context.Background(), f)
}
