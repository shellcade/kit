package sdk

import "errors"

// Admission errors returned by RoomCtl.Join.
var (
	// ErrRoomFull is returned when the room is at capacity.
	ErrRoomFull = errors.New("room is full")
	// ErrRoomClosed is returned when the room is settling or disposed.
	ErrRoomClosed = errors.New("room is closed")
)

// Credits errors a CreditsService implementation returns; the gameabi host
// maps them onto the ABI status codes for the guest.
var (
	// ErrInsufficientCredits refuses a wager over the balance or a platform
	// bet limit — the bet did not happen.
	ErrInsufficientCredits = errors.New("sdk: insufficient credits")
	// ErrEconomyDisabled reports the credits economy switched off host-side;
	// guests render an out-of-service state.
	ErrEconomyDisabled = errors.New("sdk: credits economy disabled")
	// ErrCreditsDenied refuses a call outside the rules (game-kind guest,
	// unknown seat, no open stake to settle).
	ErrCreditsDenied = errors.New("sdk: credits denied")
)

// internal aliases kept for the actor loop's brevity.
var (
	errRoomFull   = ErrRoomFull
	errRoomClosed = ErrRoomClosed
)
