//go:build wasip1

package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// Imported from the host runtime. The host reads a request from guest memory
// at [reqPtr, reqPtr+reqLen) and writes its reply to [respPtr, respPtr+respCap).
// It returns the reply length (>= 0), or a negative value when it rejects the
// request as malformed.
//
//go:wasmimport env exec_command
func _hostExec(reqPtr unsafe.Pointer, reqLen uint32, respPtr unsafe.Pointer, respCap uint32) int64

// Exec protocol, version 2. Every request starts with the magic and an
// operation, and every reply fits the buffer the shell offers, so the host
// runs each command exactly once, and the shell never holds a command's whole
// input or output: it streams stdin to the host and reads the output back in
// chunks. A command's state lives on the host under a handle from opBegin
// until opEnd.
//
//	opBegin: u32 argc, argc × (u32 len, bytes), u32 envc, envc × (u32 len, bytes)
//	         → u32 handle
//	opStdin: u32 handle, bytes → u32 1 to continue, or 0 once the host has
//	         reached its input cap and wants no more
//	opRun:   u32 handle → u32 exit code
//	opRead:  u32 handle, u32 stream (1 stdout, 2 stderr) → the next bytes of
//	         that stream, empty once it is drained
//	opEnd:   u32 handle → empty
//
// All integers are little-endian.
const (
	protocolMagic uint32 = 0x32585348 // "SHX2"

	opBegin uint32 = 1
	opStdin uint32 = 2
	opRun   uint32 = 3
	opRead  uint32 = 4
	opEnd   uint32 = 5

	streamStdout uint32 = 1
	streamStderr uint32 = 2

	// chunkSize bounds each stdin request and output reply, which keeps the
	// shell's memory independent of how much data passes through a pipeline.
	chunkSize = 64 * 1024
)

// shellNames are the commands the shell runs itself, in a fresh interpreter,
// instead of sending them to the host. A nested shell then needs no second
// Wasm instance, and it starts in the caller's working directory.
var shellNames = map[string]bool{"sh": true, "bash": true, "mksh": true, "ksh": true}

// isShell reports whether args[0] names a shell: a bare name, or one in a
// system bin directory, but not a script of the user's such as ./build/sh.
func isShell(name string) bool {
	switch path.Dir(name) {
	case ".", "/bin", "/usr/bin", "/usr/local/bin":
		return shellNames[path.Base(name)] && (path.IsAbs(name) || !strings.Contains(name, "/"))
	}
	return false
}

// hostConn issues the exec protocol calls for one command.
type hostConn struct {
	req  []byte
	resp []byte
}

// hostConnFree keeps a few idle connections, so a loop that runs many small
// commands does not allocate two chunks for each. It is bounded so that a
// pipeline of many external stages does not leave all their buffers idle for
// the rest of the call.
var hostConnFree struct {
	sync.Mutex
	conns []*hostConn
}

const maxFreeHostConns = 4

func getHostConn() *hostConn {
	hostConnFree.Lock()
	defer hostConnFree.Unlock()
	if n := len(hostConnFree.conns); n > 0 {
		c := hostConnFree.conns[n-1]
		hostConnFree.conns = hostConnFree.conns[:n-1]
		return c
	}
	return &hostConn{
		req:  make([]byte, 0, 12+chunkSize),
		resp: make([]byte, chunkSize),
	}
}

func putHostConn(c *hostConn) {
	// A large argv or environment grows the request buffer; do not keep it.
	if cap(c.req) > 12+chunkSize {
		return
	}
	hostConnFree.Lock()
	defer hostConnFree.Unlock()
	if len(hostConnFree.conns) < maxFreeHostConns {
		hostConnFree.conns = append(hostConnFree.conns, c)
	}
}

func (c *hostConn) start(op uint32) {
	c.req = binary.LittleEndian.AppendUint32(c.req[:0], protocolMagic)
	c.req = binary.LittleEndian.AppendUint32(c.req, op)
}

func (c *hostConn) u32(v uint32) {
	c.req = binary.LittleEndian.AppendUint32(c.req, v)
}

func (c *hostConn) str(s string) {
	c.u32(uint32(len(s)))
	c.req = append(c.req, s...)
}

func (c *hostConn) call() ([]byte, error) {
	n := _hostExec(
		unsafe.Pointer(&c.req[0]), uint32(len(c.req)),
		unsafe.Pointer(&c.resp[0]), uint32(len(c.resp)),
	)
	runtime.KeepAlive(c.req)
	runtime.KeepAlive(c.resp)
	if n < 0 || n > int64(len(c.resp)) {
		return nil, fmt.Errorf("exec_command: the host rejected the request (status %d); the shell and the runtime may come from different builds", n)
	}
	return c.resp[:n], nil
}

func (c *hostConn) callU32() (uint32, error) {
	reply, err := c.call()
	if err != nil {
		return 0, err
	}
	if len(reply) != 4 {
		return 0, fmt.Errorf("exec_command: expected a 4-byte reply, got %d bytes; the runtime may predate exec protocol version 2", len(reply))
	}
	return binary.LittleEndian.Uint32(reply), nil
}

func (c *hostConn) begin(args, env []string) (uint32, error) {
	c.start(opBegin)
	c.u32(uint32(len(args)))
	for _, a := range args {
		c.str(a)
	}
	c.u32(uint32(len(env)))
	for _, e := range env {
		c.str(e)
	}
	return c.callU32()
}

// sendStdin streams r to the host until EOF or until the host has enough.
// Each request carries a full chunk unless the input ends, so a producer that
// writes a line at a time does not cost a host call per line. readErr is a
// failed read of r, and err a failed call to the host.
func (c *hostConn) sendStdin(handle uint32, r io.Reader) (readErr, err error) {
	for {
		c.start(opStdin)
		c.u32(handle)
		header := len(c.req)
		n, readErr := io.ReadFull(r, c.req[header:header+chunkSize])
		if n > 0 {
			c.req = c.req[:header+n]
			more, err := c.callU32()
			if err != nil {
				return nil, err
			}
			if more == 0 {
				return nil, nil
			}
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			return nil, nil
		}
		if readErr != nil {
			return readErr, nil
		}
	}
}

func (c *hostConn) run(handle uint32) (uint32, error) {
	c.start(opRun)
	c.u32(handle)
	return c.callU32()
}

// externalWriter is implemented by the interpreter's pipeline writer, whose
// plain Write stops the whole pipeline stage when the reader has finished.
type externalWriter interface {
	WriteExternal(p []byte) (int, error)
}

// copyStream writes one of the command's output streams to w. A failed write
// stops the copy, and the host drops the rest of the stream when the command
// ends; writeErr is that write's error, and err a failed call to the host.
func (c *hostConn) copyStream(handle, stream uint32, w io.Writer) (writeErr, err error) {
	write := w.Write
	if ew, ok := w.(externalWriter); ok {
		write = ew.WriteExternal
	}
	for {
		c.start(opRead)
		c.u32(handle)
		c.u32(stream)
		chunk, err := c.call()
		if err != nil {
			return nil, err
		}
		if len(chunk) == 0 {
			return nil, nil
		}
		if _, err := write(chunk); err != nil {
			return err, nil
		}
	}
}

func (c *hostConn) end(handle uint32) {
	c.start(opEnd)
	c.u32(handle)
	c.call()
}

func hostExecHandler(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		hc := interp.HandlerCtx(ctx)
		if isShell(args[0]) {
			return startNestedShell(ctx, hc, args)
		}

		c := getHostConn()
		defer putHostConn(c)
		handle, err := c.begin(args, exportedEnv(hc.Env, hc.Dir))
		if err != nil {
			return err
		}
		defer c.end(handle)

		if hc.Stdin != nil {
			readErr, err := c.sendStdin(handle, hc.Stdin)
			if err != nil {
				return err
			}
			if readErr != nil {
				// The command would have failed to read its own input; it
				// does not run, and the script goes on.
				fmt.Fprintf(hc.Stderr, "%s: read error: %v\n", args[0], withoutPath(readErr))
				return interp.ExitStatus(1)
			}
		}
		code, err := c.run(handle)
		if err != nil {
			return err
		}
		stdoutErr, err := c.copyStream(handle, streamStdout, hc.Stdout)
		if err != nil {
			return err
		}
		stderrErr, err := c.copyStream(handle, streamStderr, hc.Stderr)
		if err != nil {
			return err
		}
		for _, writeErr := range []error{stdoutErr, stderrErr} {
			if writeErr != nil && !errors.Is(writeErr, io.ErrClosedPipe) {
				// The command has ended, so report the lost output as a
				// command that wrote it itself would.
				fmt.Fprintf(hc.Stderr, "%s: write error: %v\n", args[0], withoutPath(writeErr))
				return interp.ExitStatus(1)
			}
		}
		if stdoutErr != nil || stderrErr != nil {
			// A pipe whose reader has finished, which would have killed the
			// command with SIGPIPE: 128 + 13.
			return interp.ExitStatus(141)
		}
		// As in bash, only the low 8 bits of the status survive, so 256 is
		// success.
		if status := uint8(code); status != 0 {
			return interp.ExitStatus(status)
		}
		return nil
	}
}

// openHandler opens /dev/null as a null device, and /dev/stdin, /dev/stdout
// and /dev/stderr as the shell's current standard streams. The runtime's
// /dev holds only an ordinary file named null, so a redirect to /dev/null
// would keep what was written and read it back later in the call, and one to
// /dev/stderr would write a stray file. The name resolves as the default
// handler resolves it, so `null` in /dev and `/dev//null` count too.
func openHandler() interp.OpenHandlerFunc {
	open := interp.DefaultOpenHandler()
	return func(ctx context.Context, name string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
		hc := interp.HandlerCtx(ctx)
		resolved := name
		if resolved != "" && !filepath.IsAbs(resolved) {
			resolved = filepath.Join(hc.Dir, resolved)
		}
		switch filepath.Clean(resolved) {
		case "/dev/null":
			return devNull{}, nil
		case "/dev/stdin":
			if hc.Stdin != nil {
				return stdStream{Reader: hc.Stdin, Writer: devNull{}}, nil
			}
			return devNull{}, nil
		case "/dev/stdout":
			if hc.Stdout != nil {
				return stdStream{Reader: devNull{}, Writer: hc.Stdout}, nil
			}
			return devNull{}, nil
		case "/dev/stderr":
			if hc.Stderr != nil {
				return stdStream{Reader: devNull{}, Writer: hc.Stderr}, nil
			}
			return devNull{}, nil
		}
		return open(ctx, name, flag, perm)
	}
}

type devNull struct{}

func (devNull) Read([]byte) (int, error)    { return 0, io.EOF }
func (devNull) Write(p []byte) (int, error) { return len(p), nil }
func (devNull) Close() error                { return nil }

// stdStream is one of the shell's standard streams opened by name. Closing it
// leaves the stream open, as closing a duplicated descriptor would, and an
// external command writing through it breaks a pipe as it would writing to
// the stream directly.
type stdStream struct {
	io.Reader
	io.Writer
}

func (stdStream) Close() error { return nil }

func (s stdStream) WriteExternal(p []byte) (int, error) {
	if ew, ok := s.Writer.(externalWriter); ok {
		return ew.WriteExternal(p)
	}
	return s.Write(p)
}

// exportedEnv collects the per-command environment, including inline
// assignments such as PYTHONHOME=/usr/local/bin and exported shell variables,
// with PWD set to dir: a command enters the directory PWD names, so it
// follows the shell's directory whatever a script did to the variable.
//
// env yields a scope's variables after its parent's, so the last entry of a
// name is the variable's current state, as lookups see it: a script's unset,
// or a change that leaves the variable unexported or not a string, keeps an
// inherited value from the command. As in interp's execEnv, and Bash, a local
// variable without a value does not hide an outer one.
func exportedEnv(env expand.Environ, dir string) []string {
	values := map[string]string{}
	seen := map[string]bool{}
	var names []string
	for name, vr := range env.Each {
		switch {
		case name == "PWD", !vr.IsSet() && vr.Local:
		case vr.IsSet() && vr.Exported && vr.Kind == expand.String:
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
			values[name] = vr.String()
		default:
			delete(values, name)
		}
	}
	pairs := []string{"PWD=" + dir}
	for _, name := range names {
		if value, ok := values[name]; ok {
			pairs = append(pairs, name+"="+value)
		}
	}
	return pairs
}

// maxShellNesting bounds how deeply shells may start shells, so that a
// script that runs itself fails with an error instead of exhausting memory.
const maxShellNesting = 16

type nestingKey struct{}

// startNestedShell runs a nested shell on a goroutine of its own, as a
// separate process would run on its own stack. Go on Wasm unwinds the native
// Wasm stack whenever a goroutine blocks, so the nested shell's recursion
// does not add to the depth of the caller's.
func startNestedShell(ctx context.Context, hc interp.HandlerContext, args []string) error {
	depth, _ := ctx.Value(nestingKey{}).(int)
	if depth >= maxShellNesting {
		fmt.Fprintf(hc.Stderr, "%s: shells nested too deeply (limit %d)\n", path.Base(args[0]), maxShellNesting)
		return interp.ExitStatus(1)
	}
	ctx = context.WithValue(ctx, nestingKey{}, depth+1)
	done := make(chan error, 1)
	go func() { done <- runNestedShell(ctx, hc, args) }()
	return <-done
}

// runNestedShell runs `sh`, `bash` and their aliases in a fresh interpreter,
// as a child shell process would run: it inherits only exported variables,
// the working directory and the standard streams. It accepts
// `-c command [name [args...]]`, `script [args...]` and a script on stdin
// (`-s`, or no operands), with set options such as `-e`, `-u`, `-x` and
// `-o pipefail`.
func runNestedShell(ctx context.Context, hc interp.HandlerContext, args []string) error {
	name := path.Base(args[0])
	fail := func(status uint8, format string, a ...any) error {
		fmt.Fprintf(hc.Stderr, name+": "+format+"\n", a...)
		return interp.ExitStatus(status)
	}

	var opts []string
	commandMode, stdinMode := false, false
	rest := args[1:]
options:
	for len(rest) > 0 {
		arg := rest[0]
		switch {
		case arg == "--" || arg == "-":
			rest = rest[1:]
			break options
		case strings.HasPrefix(arg, "--"):
			rest = rest[1:]
			switch arg {
			case "--login", "--norc", "--noprofile", "--posix", "--noediting":
			case "--rcfile", "--init-file":
				// No startup file is read, so its name is only consumed.
				if len(rest) == 0 {
					return fail(2, "%s: option requires an argument", arg)
				}
				rest = rest[1:]
			case "--version":
				fmt.Fprintf(hc.Stdout, "%s (sh-wasi, a WebAssembly build of mvdan/sh)\n", name)
				return nil
			default:
				return fail(2, "%s: invalid option", arg)
			}
		case len(arg) > 1 && (arg[0] == '-' || arg[0] == '+'):
			rest = rest[1:]
			sign := arg[:1]
			for _, flag := range arg[1:] {
				switch flag {
				case 'c':
					commandMode = true
				case 's':
					stdinMode = true
				case 'l', 'i':
				case 'o':
					if len(rest) == 0 {
						return fail(2, "%so: option requires an argument", sign)
					}
					opts = append(opts, sign+"o", rest[0])
					rest = rest[1:]
				case 'O':
					// Shell options (shopt) are consumed and not applied.
					if len(rest) == 0 {
						return fail(2, "%sO: option requires an argument", sign)
					}
					rest = rest[1:]
				default:
					opts = append(opts, sign+string(flag))
				}
			}
		default:
			break options
		}
	}

	var source io.Reader
	var sourceName string
	var params []string
	switch {
	case commandMode:
		if len(rest) == 0 {
			return fail(2, "-c: option requires an argument")
		}
		source = strings.NewReader(rest[0])
		// rest[1], when present, is $0, which the interpreter does not model.
		if len(rest) > 2 {
			params = rest[2:]
		}
	case !stdinMode && len(rest) > 0:
		script := rest[0]
		if !filepath.IsAbs(script) {
			script = filepath.Join(hc.Dir, script)
		}
		data, err := os.ReadFile(script)
		if errors.Is(err, fs.ErrNotExist) {
			return fail(127, "%s: No such file or directory", rest[0])
		}
		if err != nil {
			return fail(126, "%s: %v", rest[0], err)
		}
		source = strings.NewReader(string(data))
		sourceName = rest[0]
		params = rest[1:]
	default:
		if hc.Stdin == nil {
			return nil
		}
		source = hc.Stdin
		params = rest
	}

	file, err := syntax.NewParser().Parse(source, sourceName)
	if err != nil {
		return fail(2, "%v", err)
	}
	// Writing into a pipeline stage, the nested shell gets a SIGPIPE of its
	// own: once the reader has finished, its writes stop it alone, as the
	// signal would kill only the child process and not the caller's stage.
	parent := ctx
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	pipe := &childPipe{stop: stop}
	r, err := interp.New(
		interp.StdIO(hc.Stdin, pipe.wrap(hc.Stdout), pipe.wrap(hc.Stderr)),
		interp.ExecHandlers(hostExecHandler),
		interp.OpenHandler(openHandler()),
		interp.Env(expand.ListEnviron(exportedEnv(hc.Env, hc.Dir)...)),
		interp.Params(append(append(opts, "--"), params...)...),
		// Take the calling interpreter's directory as it is, as a child
		// process inherits its parent's: the Dir option, or none, would stat
		// it or the process's start directory, and fail if either is gone.
		func(r *interp.Runner) error { r.Dir = hc.Dir; return nil },
	)
	if err != nil {
		return fail(2, "%v", err)
	}
	err = r.Run(ctx, file)
	if pipe.broken.Load() && parent.Err() == nil {
		// As if SIGPIPE had killed the child: 128 + 13.
		return interp.ExitStatus(141)
	}
	var status interp.ExitStatus
	if err == nil || errors.As(err, &status) || parent.Err() != nil {
		// A canceled caller, such as by its own pipeline's SIGPIPE stand-in,
		// stays fatal so that the caller's interpreter handles it as its own.
		return err
	}
	return fail(1, "%v", err)
}

// childPipe is a nested shell's SIGPIPE: the shell's writes into a pipeline
// stage stop the nested shell once the stage's reader has finished.
type childPipe struct {
	stop context.CancelFunc
	// Set by whichever goroutine's write found the pipe closed, including a
	// background job's.
	broken atomic.Bool
}

// wrap returns w with the nested shell's own writes routed through the
// pipe, or w itself when it is not a pipeline stage's writer.
func (c *childPipe) wrap(w io.Writer) io.Writer {
	if ew, ok := w.(externalWriter); ok {
		return childPipeWriter{pipe: c, w: ew}
	}
	return w
}

type childPipeWriter struct {
	pipe *childPipe
	w    externalWriter
}

func (c childPipeWriter) Write(p []byte) (int, error) {
	n, err := c.w.WriteExternal(p)
	if errors.Is(err, io.ErrClosedPipe) {
		c.pipe.broken.Store(true)
		c.pipe.stop()
	}
	return n, err
}

func (c childPipeWriter) WriteExternal(p []byte) (int, error) {
	return c.w.WriteExternal(p)
}
