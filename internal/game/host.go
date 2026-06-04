//go:build wasip1 || tinygo.wasm

package game

import pdk "github.com/extism/go-pdk"

// Raw imports of the shellcade host functions (ABI v1, namespace
// "extism:host/user"). Pointer-typed params/returns are Extism memory offsets.

//go:wasmimport extism:host/user send
func hostSend(playerIdx uint64, frameOff uint64)

//go:wasmimport extism:host/user identical
func hostIdentical(frameOff uint64)

//go:wasmimport extism:host/user set_input_context
func hostSetInputContext(ctx uint64)

//go:wasmimport extism:host/user end
func hostEnd(resultOff uint64)

//go:wasmimport extism:host/user post
func hostPost(resultOff uint64)

//go:wasmimport extism:host/user log
func hostLog(level uint64, msgOff uint64)

//go:wasmimport extism:host/user kv_get
func hostKVGet(playerIdx uint64, keyOff uint64) uint64

//go:wasmimport extism:host/user kv_set
func hostKVSet(playerIdx uint64, keyOff uint64, valOff uint64, ruleOff uint64)

//go:wasmimport extism:host/user kv_delete
func hostKVDelete(playerIdx uint64, keyOff uint64)

//go:wasmimport extism:host/user config_get
func hostConfigGet(keyOff uint64) uint64

// alloc copies b into Extism kernel memory; the caller MUST Free it after the
// host call returns (kernel memory is not garbage collected).
func alloc(b []byte) pdk.Memory { return pdk.AllocateBytes(b) }

func allocStr(s string) pdk.Memory { return pdk.AllocateString(s) }

// readBytesFree reads host-returned bytes (0 = not found) and frees them.
func readBytesFree(off uint64) ([]byte, bool) {
	if off == 0 {
		return nil, false
	}
	mem := pdk.FindMemory(off)
	b := mem.ReadBytes()
	mem.Free()
	return b, true
}

func inputBytes() []byte { return pdk.Input() }

func outputBytes(b []byte) { pdk.Output(b) }
