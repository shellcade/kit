//! shellcade ABI **v2** RUN-LIST frame-delta encoder — the golden-vector
//! verification harness binding the `shellcade-kit` SDK's encoder
//! BYTE-IDENTICAL to the Go reference.
//!
//! The encoder implementation lives in the SDK crate (`kit/rust`,
//! `shellcade_kit::delta`) — this crate is its harness: thin wrappers
//! preserving the original `diff-rs` signatures (epoch is modeled as 0 here;
//! the byte COUNT is epoch-independent since the field is always present and
//! fixed-width), the unit tests of the container layout, the committed golden
//! vectors from the Go reference encoder (`tests/golden/*.dgld`, emitted by
//! `kit/internal/diffbench` TestEmitGolden), and the perf sanity example.
//!
//! A frame is a fixed 24x80 grid of **24-byte grapheme cells** under the
//! canonical-zero rule (unused `cp` slots and `pad` MUST be zero, so cell
//! equality is a 24-byte `memcmp`). The normative **9-byte container header**
//! is `u8 flags` (bit0 = keyframe), `u32 epoch`, `u16 runCount`, `u8 rows=24`,
//! `u8 cols=80`, followed by `runCount` runs of `{u16 startIndex, u16 runLen,
//! runLen*24 packed cells}`. The **keyframe form** (flags bit0, one run over
//! all 1920 cells) is the bootstrap / worst case (`KEYFRAME_BYTES` = 46093).
//!
//! All encoders operate on the PACKED wire representation: `prev` and `next`
//! are packed frames, `dst` is a caller-owned reused scratch buffer, and the
//! return value is the payload byte length. None of them allocate.

use shellcade_kit::delta;

// ---- frame geometry ----------------------------------------------------------

pub const ROWS: usize = 24;
pub const COLS: usize = 80;
/// v2 grapheme cell width.
pub const CELL_BYTES: usize = 24;
pub const FRAME_CELLS: usize = ROWS * COLS; // 1920
pub const FRAME_BYTES: usize = FRAME_CELLS * CELL_BYTES; // 46080

// ---- container constants (ABI v2) ----------------------------------------------

/// Normative frame-delta container header: u8 flags, u32 epoch, u16 runCount,
/// u8 rows, u8 cols.
pub const DELTA_HEADER_BYTES: usize = delta::DELTA_HEADER_BYTES;

/// Grid geometry carried in the last two header bytes; the host validates
/// `== (ROWS, COLS)`.
pub const GEOMETRY_ROWS: u8 = delta::GEOMETRY_ROWS;
pub const GEOMETRY_COLS: u8 = delta::GEOMETRY_COLS;

/// Per-run prefix inside a run-list payload: u16 startIndex + u16 runLen.
pub const RUN_HEADER_BYTES: usize = delta::RUN_HEADER_BYTES;

/// Header flags bit0: the payload is a self-contained keyframe (full frame).
pub const FLAG_KEYFRAME: u8 = delta::FLAG_KEYFRAME;

/// Worst-case / keyframe-form size.
pub const KEYFRAME_BYTES: usize = delta::KEYFRAME_BYTES;

/// A buffer big enough for any encoder's worst case, matching the Go bench's
/// `MaxEncoded` (generous: a full cell-list-style worst case).
pub const MAX_ENCODED: usize = FRAME_BYTES + FRAME_CELLS * 2 + 8;

// ---- delegating wrappers (original diff-rs signatures, epoch = 0) ---------------

/// Coalesce changed cells into runs of CONSECUTIVE changed cells. A
/// `runCount == 0` payload (the 9-byte header alone) is the legal "no change"
/// delta. Returns the payload byte length.
pub fn encode_run_list(prev: &[u8], next: &[u8], dst: &mut [u8]) -> usize {
    delta::encode_run_list(prev, next, dst, 0)
}

/// Emit the v2 KEYFRAME FORM: header (flags bit0) + exactly ONE run covering
/// all 1920 cells + the full packed grid (`KEYFRAME_BYTES`).
pub fn encode_keyframe(_prev: &[u8], next: &[u8], dst: &mut [u8]) -> usize {
    delta::encode_keyframe(next, dst, 0)
}

/// RUN-LIST with the v2 safety valve: if the delta payload would meet or
/// exceed the keyframe form's size, ship the keyframe form instead. This is
/// the encoding a production guest ships.
pub fn encode_run_list_or_keyframe(prev: &[u8], next: &[u8], dst: &mut [u8]) -> usize {
    delta::encode(prev, next, dst, 0, false)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn blank_frame() -> Vec<u8> {
        let mut f = vec![0u8; FRAME_BYTES];
        for i in 0..FRAME_CELLS {
            f[i * CELL_BYTES..i * CELL_BYTES + 4].copy_from_slice(&(b' ' as u32).to_le_bytes());
        }
        f
    }

    fn put_rune(f: &mut [u8], i: usize, r: u32) {
        f[i * CELL_BYTES..i * CELL_BYTES + 4].copy_from_slice(&r.to_le_bytes());
    }

    /// The cell-equality the encoder is REQUIRED to implement: a 24-byte memcmp.
    fn cell_equal(a: &[u8], b: &[u8], o: usize) -> bool {
        a[o..o + CELL_BYTES] == b[o..o + CELL_BYTES]
    }

    // read header fields; the 4th/5th returns are the geometry bytes (rows, cols)
    fn hdr(dst: &[u8]) -> (u8, u32, u16, u8, u8) {
        let flags = dst[0];
        let epoch = u32::from_le_bytes([dst[1], dst[2], dst[3], dst[4]]);
        let runs = u16::from_le_bytes([dst[5], dst[6]]);
        let rows = dst[7];
        let cols = dst[8];
        (flags, epoch, runs, rows, cols)
    }

    #[test]
    fn no_change_is_header_only() {
        let f = blank_frame();
        let mut dst = vec![0u8; MAX_ENCODED];
        let n = encode_run_list(&f, &f, &mut dst);
        assert_eq!(n, DELTA_HEADER_BYTES);
        assert_eq!(hdr(&dst), (0, 0, 0, GEOMETRY_ROWS, GEOMETRY_COLS));
    }

    #[test]
    fn single_cell_change_is_one_run() {
        let prev = blank_frame();
        let mut next = prev.clone();
        put_rune(&mut next, 42, b'X' as u32);
        let mut dst = vec![0u8; MAX_ENCODED];
        let n = encode_run_list(&prev, &next, &mut dst);
        assert_eq!(n, DELTA_HEADER_BYTES + RUN_HEADER_BYTES + CELL_BYTES);
        let (flags, _, runs, _, _) = hdr(&dst);
        assert_eq!(flags, 0);
        assert_eq!(runs, 1);
        let start = u16::from_le_bytes([dst[9], dst[10]]);
        let len = u16::from_le_bytes([dst[11], dst[12]]);
        assert_eq!(start, 42);
        assert_eq!(len, 1);
        // the packed cell carries 'X'
        let rune = u32::from_le_bytes([dst[13], dst[14], dst[15], dst[16]]);
        assert_eq!(rune, b'X' as u32);
    }

    #[test]
    fn two_runs_with_gap() {
        let prev = blank_frame();
        let mut next = prev.clone();
        put_rune(&mut next, 0, b'A' as u32);
        put_rune(&mut next, 1, b'B' as u32); // run 1: cells 0..2
        put_rune(&mut next, 10, b'C' as u32); // run 2: cell 10
        let mut dst = vec![0u8; MAX_ENCODED];
        let n = encode_run_list(&prev, &next, &mut dst);
        let (_, _, runs, _, _) = hdr(&dst);
        assert_eq!(runs, 2);
        assert_eq!(n, DELTA_HEADER_BYTES + 2 * RUN_HEADER_BYTES + 3 * CELL_BYTES);
        // run 1
        assert_eq!(u16::from_le_bytes([dst[9], dst[10]]), 0);
        assert_eq!(u16::from_le_bytes([dst[11], dst[12]]), 2);
        // run 2 starts after run1 header + 2 cells
        let r2 = 9 + 4 + 2 * CELL_BYTES;
        assert_eq!(u16::from_le_bytes([dst[r2], dst[r2 + 1]]), 10);
        assert_eq!(u16::from_le_bytes([dst[r2 + 2], dst[r2 + 3]]), 1);
    }

    #[test]
    fn keyframe_form_layout() {
        let prev = blank_frame();
        let mut next = prev.clone();
        put_rune(&mut next, 5, b'Z' as u32);
        let mut dst = vec![0u8; MAX_ENCODED];
        let n = encode_keyframe(&prev, &next, &mut dst);
        assert_eq!(n, KEYFRAME_BYTES);
        let (flags, _, runs, rows, cols) = hdr(&dst);
        assert_eq!(flags, FLAG_KEYFRAME);
        assert_eq!(runs, 1);
        assert_eq!((rows, cols), (GEOMETRY_ROWS, GEOMETRY_COLS));
        assert_eq!(u16::from_le_bytes([dst[9], dst[10]]), 0); // start
        assert_eq!(u16::from_le_bytes([dst[11], dst[12]]), FRAME_CELLS as u16); // len 1920
        // full grid follows
        assert_eq!(&dst[13..13 + FRAME_BYTES], &next[..]);
    }

    #[test]
    fn fallback_picks_keyframe_on_full_change() {
        // Every cell differs -> run-list is 9 + 4 + 46080 = 46093 == KEYFRAME_BYTES,
        // which meets the threshold, so fallback ships the keyframe form.
        let mut prev = blank_frame();
        let mut next = blank_frame();
        for i in 0..FRAME_CELLS {
            put_rune(&mut prev, i, b'A' as u32);
            put_rune(&mut next, i, b'B' as u32);
        }
        let mut dst = vec![0u8; MAX_ENCODED];
        let n = encode_run_list_or_keyframe(&prev, &next, &mut dst);
        assert_eq!(n, KEYFRAME_BYTES);
        let (flags, _, _, _, _) = hdr(&dst);
        assert_eq!(flags, FLAG_KEYFRAME);
    }

    #[test]
    fn canonical_zero_pad_and_cp_slots() {
        // Two cells whose only differing bytes are in pad would be a violation of
        // the canonical-zero rule; here we assert the SDK encoder's equality is a
        // true 24-byte compare (a difference anywhere in the 24 bytes is a run).
        let prev = blank_frame();
        let mut dst = vec![0u8; MAX_ENCODED];
        for off in 0..CELL_BYTES {
            let mut next = prev.clone();
            next[off] ^= 0xFF; // perturb cell 0 at byte `off`
            assert!(!cell_equal(&prev, &next, 0), "byte {off} must register a change");
            let n = encode_run_list(&prev, &next, &mut dst);
            assert_eq!(
                n,
                DELTA_HEADER_BYTES + RUN_HEADER_BYTES + CELL_BYTES,
                "byte {off}: the encoder must emit exactly one one-cell run"
            );
        }
    }
}
