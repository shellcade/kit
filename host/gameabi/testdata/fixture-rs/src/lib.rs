//! fixture-rs — a second gameabi test guest, written in RUST from the public
//! shellcade ABI contract (kit/ABI.md + wire.go) alone.
//!
//! It proves the ABI is language-neutral: the SAME host adapter
//! (internal/gameabi) and the SAME conformance harness that drive the TinyGo
//! Go fixture also drive this artifact green. Only the CORE surface is mirrored
//! (full command parity with the Go fixture is a non-goal):
//!
//!   shellcade_abi -> u32 ABI major version (2)
//!   meta          -> slug "fixture-rs", 1..=2 players
//!   start/join/leave -> render the status frame
//!   input 'e'     -> end the room (winner = sender, metric 42)
//!   input 'p'     -> panic (guest trap; panic=abort -> wasm unreachable)
//!   wake          -> increment the wake counter, then render
//!
//! The rendered frame matches the Go fixture's layout (banner / players / wakes)
//! with the banner text FIXTURE-RS so a reader can tell the two artifacts apart.
//!
//! Transport is Extism: the kernel memory/IO plumbing comes from extism-pdk,
//! the 11 host functions are imported from the `extism:host/user` namespace, and
//! the 8 entry points are bare `extern "C"` exports returning an i32 status
//! (0 = ok) — the mechanics the Go runtime's //go:export trampolines also use.

use extism_pdk::extism;
use extism_pdk::Memory;

// ---------------------------------------------------------------------------
// Host functions (namespace `extism:host/user`). Declared as RAW wasm imports
// so scalar params (i64 playerIdx) pass as integers and pointer params pass as
// memory offsets (u64) — mirroring the host's (i64 playerIdx, ptr frame)
// signatures exactly. Only the functions the core surface calls are declared.
// ---------------------------------------------------------------------------
#[link(wasm_import_module = "extism:host/user")]
extern "C" {
    /// identical(ptr deltaContainer) -> i64 epoch: deliver one frame to every
    /// player. In ABI v2 the payload is the frame-delta container (§4.5) and the
    /// return value's low 32 bits carry the host-issued epoch the guest must
    /// stamp its baseline with; the upper 32 bits are reserved-zero.
    fn identical(frame_off: u64) -> u64;
    /// end(ptr result): settle the room exactly once.
    fn end(result_off: u64);
    /// log(i64 level, ptr msg): 0 debug · 1 info · 2 warn · 3 error.
    fn log(level: i64, msg_off: u64);
}

// ---------------------------------------------------------------------------
// Wire constants (ABI v2). Mirrors of kit/wire.
// ---------------------------------------------------------------------------
const ABI_VERSION: u32 = 2;

const ROWS: usize = 24;
const COLS: usize = 80;
const CELL_BYTES: usize = 24; // v2 grapheme cell: rune@0 cp2@4 cp3@8 fg@12 bg@16 attr@20 cont@21 pad@22
const FRAME_BYTES: usize = ROWS * COLS * CELL_BYTES; // 46080
const FRAME_CELLS: usize = ROWS * COLS; // 1920

// Frame-delta container header (§4.5): u8 flags, u32 epoch, u16 runCount, u8
// rows, u8 cols, then runs of {u16 startIndex, u16 runLen, runLen*CELL_BYTES}.
const DELTA_HEADER_BYTES: usize = 9;
const RUN_HEADER_BYTES: usize = 4;
const FLAG_KEYFRAME: u8 = 0x01;
// Keyframe form = header + one run {0, 1920} + the full grid = 46093 bytes.
const KEYFRAME_BYTES: usize = DELTA_HEADER_BYTES + RUN_HEADER_BYTES + FRAME_BYTES;

const INPUT_RUNE: u8 = 0; // input kind: printable rune
const STATUS_FINISHED: u8 = 0; // ranking status

// ---------------------------------------------------------------------------
// Per-room state. One plugin instance == one room, so a single mutable global
// holds the entire room state in linear memory (the ABI's state model). Guest
// code only ever runs serially inside a host callback, so this is sound.
// ---------------------------------------------------------------------------
struct Room {
    wakes: i64,
    players: usize,
    // Host-issued broadcast epoch, mirrored from the last identical() return so
    // each container stamps the epoch the host expects. This hand-rolled guest
    // always ships a KEYFRAME (the host accepts a keyframe regardless of epoch
    // and adopts the header epoch), so mirroring keeps the stamp consistent but
    // is not strictly required for correctness.
    epoch: u32,
}

static mut ROOM: Room = Room {
    wakes: 0,
    players: 0,
    epoch: 0,
};

#[allow(static_mut_refs)]
fn room() -> &'static mut Room {
    // SAFETY: callbacks are invoked serially per room (ABI §1); there is never
    // concurrent access to ROOM.
    unsafe { &mut ROOM }
}

// ---------------------------------------------------------------------------
// Little-endian append encoder (the wire format is all little-endian).
// ---------------------------------------------------------------------------
struct Buf {
    b: Vec<u8>,
}

impl Buf {
    fn new() -> Self {
        Buf { b: Vec::new() }
    }
    fn u8(&mut self, v: u8) {
        self.b.push(v);
    }
    fn u16(&mut self, v: u16) {
        self.b.extend_from_slice(&v.to_le_bytes());
    }
    fn u32(&mut self, v: u32) {
        self.b.extend_from_slice(&v.to_le_bytes());
    }
    fn i64(&mut self, v: i64) {
        self.b.extend_from_slice(&v.to_le_bytes());
    }
    fn str(&mut self, s: &str) {
        let bytes = s.as_bytes();
        let n = bytes.len().min(0xffff);
        self.u16(n as u16);
        self.b.extend_from_slice(&bytes[..n]);
    }
}

// ---------------------------------------------------------------------------
// Bounds-checked little-endian decoder (matches wire.Rd semantics: short reads
// degrade to zero/empty and set the bad flag rather than panicking).
// ---------------------------------------------------------------------------
struct Rd<'a> {
    b: &'a [u8],
    off: usize,
    bad: bool,
}

impl<'a> Rd<'a> {
    fn new(b: &'a [u8]) -> Self {
        Rd { b, off: 0, bad: false }
    }
    fn ok(&mut self, n: usize) -> bool {
        if self.bad || self.off + n > self.b.len() {
            self.bad = true;
            return false;
        }
        true
    }
    fn u8(&mut self) -> u8 {
        if !self.ok(1) {
            return 0;
        }
        let v = self.b[self.off];
        self.off += 1;
        v
    }
    fn u16(&mut self) -> u16 {
        if !self.ok(2) {
            return 0;
        }
        let v = u16::from_le_bytes([self.b[self.off], self.b[self.off + 1]]);
        self.off += 2;
        v
    }
    fn u32(&mut self) -> u32 {
        if !self.ok(4) {
            return 0;
        }
        let mut a = [0u8; 4];
        a.copy_from_slice(&self.b[self.off..self.off + 4]);
        self.off += 4;
        u32::from_le_bytes(a)
    }
    fn i64(&mut self) -> i64 {
        if !self.ok(8) {
            return 0;
        }
        let mut a = [0u8; 8];
        a.copy_from_slice(&self.b[self.off..self.off + 8]);
        self.off += 8;
        i64::from_le_bytes(a)
    }
    fn skip_str(&mut self) {
        let n = self.u16() as usize;
        if self.ok(n) {
            self.off += n;
        }
    }
}

/// Decoded slice of the CallContext (§4.1) the core surface needs. We read just
/// the member count (for `players=`) and skip the rest of each member entry.
struct Ctx {
    member_count: usize,
}

fn decode_ctx<'a>(input: &'a [u8]) -> (Ctx, Rd<'a>) {
    let mut r = Rd::new(input);
    r.i64(); // nowUnixNanos
    r.i64(); // seed
    r.u8(); // seedSet
    r.u8(); // mode
    r.u16(); // capacity
    r.u16(); // minPlayers
    let n = r.u16() as usize; // memberCount
    for _ in 0..n {
        if r.bad {
            break;
        }
        r.skip_str(); // handle
        r.skip_str(); // accountID
        r.skip_str(); // conn
        r.u8(); // kind
    }
    r.u8(); // settled
    (Ctx { member_count: n }, r)
}

// ---------------------------------------------------------------------------
// Frame composition. A frame is ROWS*COLS cells of CELL_BYTES each (§4.3); we
// write ASCII text into a zeroed buffer. Unset fg/bg/attr/cont are all 0, so a
// zero buffer is already a blank frame.
// ---------------------------------------------------------------------------
fn blank_frame() -> Vec<u8> {
    vec![0u8; FRAME_BYTES]
}

/// put_cell writes a single rune into the frame at (row, col). The 24-byte v2
/// cell is canonical-zero: only the base rune is set; cp2/cp3 (4..12), fg/bg
/// (12..20), attr/cont (20..22) and pad (22..24) stay zero.
fn put_cell(buf: &mut [u8], row: usize, col: usize, rune: u32) {
    if row >= ROWS || col >= COLS {
        return;
    }
    let o = (row * COLS + col) * CELL_BYTES;
    buf[o..o + 4].copy_from_slice(&rune.to_le_bytes());
    // bytes 4..24 stay zero (canonical-zero rule).
}

/// text writes an ASCII string starting at (row, col), one cell per byte.
fn text(buf: &mut [u8], row: usize, col: usize, s: &str) {
    for (i, ch) in s.bytes().enumerate() {
        put_cell(buf, row, col + i, ch as u32);
    }
}

/// render composes and broadcasts the status frame: banner, player count, wake
/// count — the Go fixture's layout, with a distinct banner.
fn render() {
    let rm = room();
    let mut f = blank_frame();
    text(&mut f, 0, 0, "FIXTURE-RS");
    text(&mut f, 1, 0, &format!("players={}", rm.players));
    text(&mut f, 2, 0, &format!("wakes={}", rm.wakes));
    send_identical(&f);
}

/// keyframe_container wraps a full packed grid in the v2 frame-delta KEYFRAME
/// form (§4.5): a 9-byte header {flags bit0, epoch, runCount=1, rows=24, cols=80}
/// + one run {startIndex=0, runLen=1920} + the 46080-byte grid = 46093 bytes.
/// This hand-rolled guest always ships a keyframe: the host accepts a keyframe
/// regardless of epoch and adopts the header epoch, so it is always correct
/// (and is judged on reconstructed frames, not wire-byte equality with the
/// reference encoder). epoch is the host's last-issued broadcast epoch.
fn keyframe_container(frame: &[u8], epoch: u32) -> Vec<u8> {
    let mut c = Vec::with_capacity(KEYFRAME_BYTES);
    c.push(FLAG_KEYFRAME); // u8 flags: keyframe
    c.extend_from_slice(&epoch.to_le_bytes()); // u32 epoch
    c.extend_from_slice(&1u16.to_le_bytes()); // u16 runCount = 1
    c.push(ROWS as u8); // u8 rows = 24
    c.push(COLS as u8); // u8 cols = 80
    c.extend_from_slice(&0u16.to_le_bytes()); // u16 startIndex = 0
    c.extend_from_slice(&(FRAME_CELLS as u16).to_le_bytes()); // u16 runLen = 1920
    c.extend_from_slice(frame); // 1920 * 24 bytes
    c
}

/// send_identical wraps the frame in a keyframe container, allocates it into
/// Extism memory, calls the host `identical`, mirrors the returned epoch (low 32
/// bits), then frees the block (kernel memory is not GC'd).
fn send_identical(frame: &[u8]) {
    let epoch = room().epoch;
    let container = keyframe_container(frame, epoch);
    let mem = Memory::from_bytes(&container).expect("alloc frame");
    let off = mem.offset();
    let returned = unsafe { identical(off) };
    mem.free();
    room().epoch = returned as u32; // low 32 bits carry the epoch; upper is reserved-zero
}

/// host_log emits a guest log line at info level (level 1).
fn host_log(msg: &str) {
    let mem = Memory::from_bytes(msg).expect("alloc log");
    let off = mem.offset();
    unsafe { log(1, off) };
    mem.free();
}

/// end_room settles the room with a single ranking (winner = the given roster
/// index, metric 42, rank 1, finished) — the Go fixture's 'e' result.
fn end_room(player_idx: u32, metric: i64) {
    let mut w = Buf::new();
    w.u16(1); // rankingCount
    w.u32(player_idx); // playerIdx
    w.i64(metric); // metric
    w.u16(1); // rank
    w.u8(STATUS_FINISHED); // status
    let mem = Memory::from_bytes(&w.b).expect("alloc result");
    let off = mem.offset();
    unsafe { end(off) };
    mem.free();
}

// ---------------------------------------------------------------------------
// Input helpers.
// ---------------------------------------------------------------------------
fn read_input() -> Vec<u8> {
    unsafe { extism::load_input() }
}

/// write_output stores a byte slice as the export's output value.
fn write_output(b: &[u8]) {
    let mem = Memory::from_bytes(b).expect("alloc output");
    mem.set_output();
}

// ---------------------------------------------------------------------------
// The 8 ABI exports. Bare `extern "C" fn() -> i32` (0 = ok), matching the Go
// runtime's //go:export int32 trampolines. They read input via the Extism
// kernel and drive the host effect functions directly.
// ---------------------------------------------------------------------------

/// shellcade_abi: 4 bytes, u32 ABI major version (little-endian).
#[no_mangle]
pub extern "C" fn shellcade_abi() -> i32 {
    write_output(&ABI_VERSION.to_le_bytes());
    0
}

/// meta: packed Meta (§4.2). slug "fixture-rs", 1..=2 players, no leaderboard.
#[no_mangle]
pub extern "C" fn meta() -> i32 {
    let mut w = Buf::new();
    w.str("fixture-rs"); // slug
    w.str("Fixture (Rust)"); // name
    w.str("gameabi rust test guest"); // shortDescription
    w.u16(1); // minPlayers
    w.u16(2); // maxPlayers
    w.u16(0); // tagCount
    w.str(""); // quickModeLabel
    w.str(""); // soloModeLabel
    w.str(""); // privateInviteLine
    w.u8(0); // hasLeaderboard = false
    write_output(&w.b);
    0
}

/// start: render the opening frame.
#[no_mangle]
pub extern "C" fn start() -> i32 {
    let input = read_input();
    let (ctx, _) = decode_ctx(&input);
    room().players = ctx.member_count;
    render();
    0
}

/// join: roster grew; re-render.
#[no_mangle]
pub extern "C" fn join() -> i32 {
    let input = read_input();
    let (ctx, _) = decode_ctx(&input);
    room().players = ctx.member_count;
    render();
    0
}

/// leave: roster shrank (the departed player is the final roster entry, so the
/// living member count is memberCount-1); re-render.
#[no_mangle]
pub extern "C" fn leave() -> i32 {
    let input = read_input();
    let (ctx, _) = decode_ctx(&input);
    // On leave the departed player is appended as the final roster entry (§2),
    // so the living count is one less.
    room().players = ctx.member_count.saturating_sub(1);
    render();
    0
}

/// input: Ctx ‖ u32 playerIdx ‖ u8 kind ‖ u32 rune ‖ u8 key. We act on
/// printable runes: 'e' ends the room, 'p' panics (trap), anything else
/// re-renders.
#[no_mangle]
pub extern "C" fn input() -> i32 {
    let input = read_input();
    let (_ctx, mut r) = decode_ctx(&input);
    let player_idx = r.u32();
    let kind = r.u8();
    let rune = r.u32();
    let _key = r.u8();
    if kind == INPUT_RUNE {
        match char::from_u32(rune) {
            Some('e') => {
                host_log("fixture-rs: end");
                end_room(player_idx, 42);
                return 0;
            }
            Some('p') => {
                // Deliberate guest trap. panic=abort lowers this to a wasm
                // `unreachable`, which the host settles as a fault.
                panic!("fixture-rs: deliberate panic");
            }
            _ => {}
        }
    }
    render();
    0
}

/// wake: the host heartbeat. Increment the wake counter, then render.
#[no_mangle]
pub extern "C" fn wake() -> i32 {
    let _input = read_input();
    room().wakes += 1;
    render();
    0
}

/// close: room teardown. Nothing to release beyond linear memory (reclaimed by
/// instance destruction); no-op.
#[no_mangle]
pub extern "C" fn close() -> i32 {
    let _input = read_input();
    0
}
