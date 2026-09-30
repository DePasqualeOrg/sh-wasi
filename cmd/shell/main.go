// shell is a non-interactive shell for WASI environments.
// It parses and executes shell commands, dispatching external
// commands through a pluggable ExecHandler.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	hasCommand := false
	var scriptArgs []string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			if i+1 >= len(args) {
				return fmt.Errorf("shell: -c requires an argument")
			}
			command, hasCommand = args[i+1], true
			scriptArgs = args[i+2:]
			break
		}
	}

	// Go on wasip1 starts in $PWD without checking it, or in its first
	// preopen when PWD is unset, which need not be /. A $PWD the shell cannot
	// enter ends it before the script runs: WASI cannot start it in a
	// directory that is gone, and running the script elsewhere would point
	// its relative paths at another tree.
	// Without one, start in $HOME, reporting one that does not exist, else in
	// / when something is mounted there, and otherwise where Go started.
	if dir := os.Getenv("PWD"); dir != "" {
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("shell: could not chdir to %s: %v", dir, withoutPath(err))
		}
	} else {
		started := false
		if home := os.Getenv("HOME"); home != "" {
			if err := os.Chdir(home); err != nil {
				fmt.Fprintf(os.Stderr, "shell: could not chdir to %s: %v\n", home, withoutPath(err))
			} else {
				started = true
			}
		}
		if !started {
			_ = os.Chdir("/")
		}
	}

	r, err := interp.New(
		interp.StdIO(os.Stdin, os.Stdout, os.Stderr),
		interp.ExecHandlers(hostExecHandler),
		interp.OpenHandler(openHandler()),
		interp.Params(scriptArgs...),
	)
	if err != nil {
		return err
	}

	// An empty -c script runs nothing, as in other shells, rather than
	// reading one from standard input.
	if hasCommand {
		return execute(r, strings.NewReader(command), "")
	}
	// No -c flag: read from stdin
	return execute(r, os.Stdin, "")
}

func execute(r *interp.Runner, reader io.Reader, name string) error {
	p := syntax.NewParser()
	f, err := p.Parse(reader, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return interp.ExitStatus(2) // like Bash with a syntax error
	}
	return r.Run(context.Background(), f)
}

// withoutPath drops the operation and path from a path error, for messages
// that already name the file.
func withoutPath(err error) error {
	if pathErr := (*fs.PathError)(nil); errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
