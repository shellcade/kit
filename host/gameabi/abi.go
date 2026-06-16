// Package gameabi is the host side of the shellcade wasm game ABI: the Extism
// (wazero) host adapter that makes a .wasm artifact satisfy sdk.Game/sdk.Handler,
// and the host functions exposing the Room effect/read surface to the guest.
//
// The ABI itself — version, names, packed payload encodings — is owned by the
// PUBLIC gamekit module (github.com/shellcade/kit/v2/wire); this package maps
// wire types onto the engine's sdk/canvas types.
package gameabi

import (
	"time"

	"github.com/shellcade/kit/v2/wire"
)

// Version is the ABI major version this host implements.
const Version = wire.Version

// WireRevision is the kit wire revision this host was compiled against
// (wire.Revision): the monotonic counter of wire-visible minor additions
// within the ABI major. Re-exported here because wire imports are confined
// to this package (the anti-corruption layer) — the catalog compares an
// artifact's declared meta revision (sdk.GameMeta.WireRevision) against it
// and warns when an artifact is ahead of the host (deploy-order skew).
const WireRevision = wire.Revision

// Heartbeat is the default host-owned wake cadence (admin-tunable per game in
// production; a flag in the devkit).
const Heartbeat = 50 * time.Millisecond
