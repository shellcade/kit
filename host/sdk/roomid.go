package sdk

import "github.com/google/uuid"

// NewRoomID mints a room identifier: a UUIDv7 string — globally unique with no
// cross-machine coordination, stable across process restarts, and time-ordered
// (so it indexes well as the directory primary key and sorts by creation time
// as the checkpoint key prefix). uuid.NewV7 only errors on reader entropy
// failure, which is unrecoverable, so this treats it like uuid.Must and panics
// rather than returning an error.
func NewRoomID() string {
	id, err := uuid.NewV7()
	if err != nil {
		panic("sdk: room id entropy failure: " + err.Error())
	}
	return id.String()
}

// ValidRoomID reports whether s is a well-formed room id — a UUID of version 7
// in canonical form. It rejects the legacy "<slug>-<seq>" id format, UUIDs of
// other versions, and the non-canonical spellings uuid.Parse otherwise accepts
// (braced, urn:uuid: prefix, undashed, upper-case): the id is a directory
// primary key, so requiring id.String() == s keeps one byte-exact key per room.
func ValidRoomID(s string) bool {
	id, err := uuid.Parse(s)
	if err != nil {
		return false
	}
	return id.Version() == 7 && id.String() == s
}
