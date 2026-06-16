//go:build wasip1 || tinygo.wasm

package main

import kit "github.com/shellcade/kit/v2"

func init() { kit.Run(Game{}) }

// The eight ABI exports, trampolined to the gamekit SDK.

//go:export shellcade_abi
func expABI() int32 { return kit.ExportABI() }

//go:export meta
func expMeta() int32 { return kit.ExportMeta() }

//go:export start
func expStart() int32 { return kit.ExportStart() }

//go:export join
func expJoin() int32 { return kit.ExportJoin() }

//go:export leave
func expLeave() int32 { return kit.ExportLeave() }

//go:export input
func expInput() int32 { return kit.ExportInput() }

//go:export wake
func expWake() int32 { return kit.ExportWake() }

//go:export close
func expClose() int32 { return kit.ExportClose() }
