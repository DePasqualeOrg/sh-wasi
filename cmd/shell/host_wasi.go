//go:build wasip1

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"runtime"
	"unsafe"

	"mvdan.cc/sh/v3/interp"
)

// Imported from the host runtime. The host reads the serialized request
// from guest memory at [reqPtr, reqPtr+reqLen), executes the command,
// and writes the serialized response to [respPtr, respPtr+respCap).
//
// Returns the actual response length on success (>= 0).
// Returns the negated required capacity (< 0) if the response buffer
// is too small, so the caller can retry with a larger buffer.
//
//go:wasmimport env exec_command
func _hostExec(reqPtr unsafe.Pointer, reqLen uint32, respPtr unsafe.Pointer, respCap uint32) int64

func hostExecHandler(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		hc := interp.HandlerCtx(ctx)

		var stdinData []byte
		if hc.Stdin != nil {
			var err error
			stdinData, err = io.ReadAll(hc.Stdin)
			if err != nil {
				return fmt.Errorf("reading stdin: %w", err)
			}
		}

		// Collect the per-command environment (includes inline vars
		// like PYTHONHOME=/usr/local/bin and exported shell variables).
		var env []string
		for name, vr := range hc.Env.Each {
			if vr.Exported && vr.IsSet() {
				env = append(env, name+"="+vr.String())
			}
		}

		req := marshalRequest(args, env, stdinData)

		respCap := uint32(65536)
		for {
			resp := make([]byte, respCap)
			n := _hostExec(
				unsafe.Pointer(&req[0]), uint32(len(req)),
				unsafe.Pointer(&resp[0]), respCap,
			)
			runtime.KeepAlive(req)
			runtime.KeepAlive(resp)

			if n < 0 {
				respCap = uint32(-n)
				continue
			}

			exitCode, stdout, stderr := unmarshalResponse(resp[:n])
			if len(stdout) > 0 {
				hc.Stdout.Write(stdout)
			}
			if len(stderr) > 0 {
				hc.Stderr.Write(stderr)
			}
			if exitCode != 0 {
				return interp.ExitStatus(exitCode)
			}
			return nil
		}
	}
}

// Request format (little-endian):
//
//	u32 argc
//	for each arg: u32 len, [len bytes]
//	u32 envc
//	for each env: u32 len, [len bytes]   (KEY=VALUE strings)
//	u32 stdin_len, [stdin_len bytes]
func marshalRequest(args []string, env []string, stdin []byte) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint32(len(args)))
	for _, arg := range args {
		binary.Write(&buf, binary.LittleEndian, uint32(len(arg)))
		buf.WriteString(arg)
	}
	binary.Write(&buf, binary.LittleEndian, uint32(len(env)))
	for _, e := range env {
		binary.Write(&buf, binary.LittleEndian, uint32(len(e)))
		buf.WriteString(e)
	}
	binary.Write(&buf, binary.LittleEndian, uint32(len(stdin)))
	buf.Write(stdin)
	return buf.Bytes()
}

// Response format (little-endian):
//
//	u32 exit_code
//	u32 stdout_len, [stdout_len bytes]
//	u32 stderr_len, [stderr_len bytes]
func unmarshalResponse(data []byte) (exitCode uint8, stdout, stderr []byte) {
	r := bytes.NewReader(data)

	var code uint32
	binary.Read(r, binary.LittleEndian, &code)
	exitCode = uint8(code)

	var n uint32
	binary.Read(r, binary.LittleEndian, &n)
	stdout = make([]byte, n)
	r.Read(stdout)

	binary.Read(r, binary.LittleEndian, &n)
	stderr = make([]byte, n)
	r.Read(stderr)
	return
}
