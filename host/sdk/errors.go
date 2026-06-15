package sdk

import "errors"

// Admission errors returned by RoomCtl.Join.
var (
	// ErrRoomFull is returned when the room is at capacity.
	ErrRoomFull = errors.New("room is full")
	// ErrRoomClosed is returned when the room is settling or disposed.
	ErrRoomClosed = errors.New("room is closed")
)

// internal aliases kept for the actor loop's brevity.
var (
	errRoomFull   = ErrRoomFull
	errRoomClosed = ErrRoomClosed
)
