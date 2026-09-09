// Package ydoc lets the server interpret Yjs documents.
//
// Until now the server relayed update bytes it could not read: saves were
// client-authored, the journal was never compacted, and nothing but a browser
// could edit a document. This package removes that limitation by running yrs,
// the Yjs organisation's Rust port, compiled to WebAssembly.
//
// wazero is a pure-Go WebAssembly runtime, so this needs no cgo and the server
// stays a single static binary with the module embedded beside the frontend.
//
// The CRDT is a second implementation of a format the browsers already speak,
// so it has to agree with them exactly rather than merely produce something
// valid. conformance_test.go is what holds that line.
package ydoc

import (
	"context"
	_ "embed"
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Rebuild with mise run build:ydoc after changing anything under shim/.
//
//go:embed ydoc.wasm
var module []byte

// status byte at the head of every result, matching src/lib.rs.
const (
	statusOK  = 0
	statusErr = 1
)

// Engine runs the WebAssembly module. Compiling is expensive and instantiating
// is not, so the compiled module is shared and each call gets an instance of
// its own: linear memory is mutable state, and one instance cannot serve two
// callers at once.
type Engine struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	config   wazero.ModuleConfig

	mu   sync.Mutex
	next int // makes every instance name unique
}

// New compiles the module. One Engine per process is enough.
func New(ctx context.Context) (*Engine, error) {
	runtime := wazero.NewRuntime(ctx)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("instantiate wasi: %w", err)
	}
	compiled, err := runtime.CompileModule(ctx, module)
	if err != nil {
		runtime.Close(ctx)
		return nil, fmt.Errorf("compile ydoc module: %w", err)
	}
	return &Engine{
		runtime:  runtime,
		compiled: compiled,
		// The module reads no files and takes no arguments; give it nothing.
		config: wazero.NewModuleConfig().WithName(""),
	}, nil
}

// Close releases the runtime.
func (e *Engine) Close(ctx context.Context) error { return e.runtime.Close(ctx) }

// call instantiates the module, hands the body to fn, and tears it down. The
// instance is discarded afterwards rather than pooled, so no document state
// can leak from one call into the next.
func (e *Engine) call(ctx context.Context, fn func(*instance) (uint64, error)) ([]byte, error) {
	e.mu.Lock()
	e.next++
	name := fmt.Sprintf("ydoc-%d", e.next)
	e.mu.Unlock()

	mod, err := e.runtime.InstantiateModule(ctx, e.compiled, e.config.WithName(name))
	if err != nil {
		return nil, fmt.Errorf("instantiate ydoc module: %w", err)
	}
	defer mod.Close(ctx)

	in := &instance{ctx: ctx, mod: mod}
	packed, err := fn(in)
	if err != nil {
		return nil, err
	}
	return in.result(packed)
}

// instance is one live module and the helpers for moving bytes across the
// boundary.
type instance struct {
	ctx context.Context
	mod api.Module
}

func (i *instance) fn(name string) (api.Function, error) {
	f := i.mod.ExportedFunction(name)
	if f == nil {
		return nil, fmt.Errorf("ydoc module exports no %s", name)
	}
	return f, nil
}

// write copies bytes into linear memory and returns their location. An empty
// slice is passed as a null pointer, which the module reads as empty.
func (i *instance) write(body []byte) (ptr, length uint32, err error) {
	if len(body) == 0 {
		return 0, 0, nil
	}
	alloc, err := i.fn("canvas_alloc")
	if err != nil {
		return 0, 0, err
	}
	out, err := alloc.Call(i.ctx, uint64(len(body)))
	if err != nil {
		return 0, 0, fmt.Errorf("allocate %d bytes: %w", len(body), err)
	}
	ptr = uint32(out[0])
	if !i.mod.Memory().Write(ptr, body) {
		return 0, 0, fmt.Errorf("write %d bytes at %d: outside memory", len(body), ptr)
	}
	return ptr, uint32(len(body)), nil
}

// result unpacks a returned (pointer, length), copies the payload out, frees
// it, and turns the module's own error reports into Go errors.
func (i *instance) result(packed uint64) ([]byte, error) {
	ptr, length := uint32(packed>>32), uint32(packed)
	if length == 0 {
		return nil, fmt.Errorf("ydoc returned an empty result")
	}
	body, ok := i.mod.Memory().Read(ptr, length)
	if !ok {
		return nil, fmt.Errorf("read %d bytes at %d: outside memory", length, ptr)
	}
	// Copy before freeing: the slice above aliases linear memory.
	payload := make([]byte, length-1)
	copy(payload, body[1:])
	status := body[0]

	if free, err := i.fn("canvas_dealloc"); err == nil {
		free.Call(i.ctx, uint64(ptr), uint64(length))
	}

	switch status {
	case statusOK:
		return payload, nil
	case statusErr:
		return nil, fmt.Errorf("ydoc: %s", payload)
	default:
		return nil, fmt.Errorf("ydoc returned status %d", status)
	}
}

// Merge collapses a sequence of updates into a single update carrying the
// whole document. This is what compaction stores in place of the journal rows
// it replaces.
func (e *Engine) Merge(ctx context.Context, updates [][]byte) ([]byte, error) {
	// Each update is prefixed with its length, which is the framing the module
	// expects; see canvas_merge_updates.
	var framed []byte
	for _, update := range updates {
		framed = binary.LittleEndian.AppendUint32(framed, uint32(len(update)))
		framed = append(framed, update...)
	}
	return e.call(ctx, func(in *instance) (uint64, error) {
		ptr, length, err := in.write(framed)
		if err != nil {
			return 0, err
		}
		fn, err := in.fn("canvas_merge_updates")
		if err != nil {
			return 0, err
		}
		out, err := fn.Call(ctx, uint64(ptr), uint64(length))
		if err != nil {
			return 0, fmt.Errorf("merge updates: %w", err)
		}
		return out[0], nil
	})
}

// Text returns the contents of one named Y.Text within an encoded state.
func (e *Engine) Text(ctx context.Context, state []byte, name string) (string, error) {
	body, err := e.call(ctx, func(in *instance) (uint64, error) {
		statePtr, stateLen, err := in.write(state)
		if err != nil {
			return 0, err
		}
		namePtr, nameLen, err := in.write([]byte(name))
		if err != nil {
			return 0, err
		}
		fn, err := in.fn("canvas_text")
		if err != nil {
			return 0, err
		}
		out, err := fn.Call(ctx, uint64(statePtr), uint64(stateLen), uint64(namePtr), uint64(nameLen))
		if err != nil {
			return 0, fmt.Errorf("read text: %w", err)
		}
		return out[0], nil
	})
	return string(body), err
}

// SetText edits a named Y.Text until it reads as next, and returns only the
// update that change produced — what the caller journals and broadcasts.
//
// This is an edit, not a replacement: the shared prefix and suffix are left
// alone, so a peer editing elsewhere in the document keeps their work. Passing
// the whole document is how a caller that thinks in text rather than in CRDT
// operations, such as an MCP client, expresses a change.
func (e *Engine) SetText(ctx context.Context, state []byte, name, next string) ([]byte, error) {
	return e.call(ctx, func(in *instance) (uint64, error) {
		statePtr, stateLen, err := in.write(state)
		if err != nil {
			return 0, err
		}
		namePtr, nameLen, err := in.write([]byte(name))
		if err != nil {
			return 0, err
		}
		nextPtr, nextLen, err := in.write([]byte(next))
		if err != nil {
			return 0, err
		}
		fn, err := in.fn("canvas_set_text")
		if err != nil {
			return 0, err
		}
		out, err := fn.Call(ctx,
			uint64(statePtr), uint64(stateLen),
			uint64(namePtr), uint64(nameLen),
			uint64(nextPtr), uint64(nextLen))
		if err != nil {
			return 0, fmt.Errorf("set text: %w", err)
		}
		return out[0], nil
	})
}
