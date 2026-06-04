//go:build wasip1 || tinygo.wasm

package main

import gamekit "github.com/shellcade/gamekit"

func init() { gamekit.Run(Game{}) }

// The eight ABI exports, trampolined to the gamekit SDK.

//go:export shellcade_abi
func expABI() int32 { return gamekit.ExportABI() }

//go:export meta
func expMeta() int32 { return gamekit.ExportMeta() }

//go:export start
func expStart() int32 { return gamekit.ExportStart() }

//go:export join
func expJoin() int32 { return gamekit.ExportJoin() }

//go:export leave
func expLeave() int32 { return gamekit.ExportLeave() }

//go:export input
func expInput() int32 { return gamekit.ExportInput() }

//go:export wake
func expWake() int32 { return gamekit.ExportWake() }

//go:export close
func expClose() int32 { return gamekit.ExportClose() }
