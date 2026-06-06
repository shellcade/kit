//! The ABI v2 frame-delta container (ABI.md §4.5) — the frame payload of every
//! `send`/`identical`. This is the byte-verified RUN-LIST encoder, moved here
//! from `kit/crossverify` (which remains its golden-vector verification
//! harness, path-depending on this crate): maximal-greedy runs, inclusive
//! keyframe threshold, always-present fixed-width epoch, canonical-zero cells.
//!
//! 9-byte header: `u8 flags` (bit0 = keyframe), `u32 epoch` (host-issued; the
//! guest stamps the epoch the host last returned for the slot, 0 on a fresh
//! instance), `u16 runCount`, `u8 rows = 24`, `u8 cols = 80`. Then `runCount`
//! runs of `{u16 startIndex, u16 runLen, runLen * 24 packed cells}`, each a
//! maximal span of CONSECUTIVE changed cells (greedy left-to-right, gap = 0).
//!
//! Not author-facing: `#[doc(hidden)]` public only so the crossverify harness
//! can bind this exact encoder byte-identical to the Go reference.

use crate::frame::{CELL_BYTES, FRAME_BYTES, FRAME_CELLS};

/// 9-byte container header.
pub const DELTA_HEADER_BYTES: usize = 9;
/// Per-run prefix: u16 startIndex + u16 runLen.
pub const RUN_HEADER_BYTES: usize = 4;
/// Header flags bit0: the payload is a self-contained keyframe (full frame).
pub const FLAG_KEYFRAME: u8 = 0x01;
/// Keyframe form / worst-case size: header + one run over all 1920 cells + grid.
pub const KEYFRAME_BYTES: usize = DELTA_HEADER_BYTES + RUN_HEADER_BYTES + FRAME_BYTES; // 46093

/// Grid geometry carried in the last two header bytes (host validates `(24,80)`).
pub const GEOMETRY_ROWS: u8 = crate::frame::ROWS as u8;
pub const GEOMETRY_COLS: u8 = crate::frame::COLS as u8;

/// Write the normative 9-byte header into `dst[0..9]`.
#[inline]
fn put_header(dst: &mut [u8], keyframe: bool, epoch: u32, run_count: usize) {
    dst[0] = if keyframe { FLAG_KEYFRAME } else { 0 };
    dst[1..5].copy_from_slice(&epoch.to_le_bytes());
    dst[5..7].copy_from_slice(&(run_count as u16).to_le_bytes());
    dst[7] = GEOMETRY_ROWS;
    dst[8] = GEOMETRY_COLS;
}

/// Whether the 24-byte cell at offset `o` is identical in `a` and `b`. Under
/// the canonical-zero rule this IS a 24-byte memcmp.
#[inline]
fn cell_equal(a: &[u8], b: &[u8], o: usize) -> bool {
    a[o..o + CELL_BYTES] == b[o..o + CELL_BYTES]
}

/// Encode the maximal-greedy RUN-LIST delta from `prev` to `next` into `dst`.
/// Returns the payload byte length. A `runCount == 0` payload (the header
/// alone) is the canonical "no change" delta. Allocates nothing.
pub fn encode_run_list(prev: &[u8], next: &[u8], dst: &mut [u8], epoch: u32) -> usize {
    debug_assert!(prev.len() >= FRAME_BYTES && next.len() >= FRAME_BYTES);
    let mut p = DELTA_HEADER_BYTES;
    let mut runs = 0usize;
    let mut i = 0usize;
    while i < FRAME_CELLS {
        if cell_equal(prev, next, i * CELL_BYTES) {
            i += 1;
            continue;
        }
        let start = i;
        while i < FRAME_CELLS && !cell_equal(prev, next, i * CELL_BYTES) {
            i += 1;
        }
        let run_len = i - start;
        dst[p..p + 2].copy_from_slice(&(start as u16).to_le_bytes());
        dst[p + 2..p + 4].copy_from_slice(&(run_len as u16).to_le_bytes());
        p += RUN_HEADER_BYTES;
        let src = start * CELL_BYTES;
        let n = run_len * CELL_BYTES;
        dst[p..p + n].copy_from_slice(&next[src..src + n]);
        p += n;
        runs += 1;
    }
    put_header(dst, false, epoch, runs);
    debug_assert_eq!(p, DELTA_HEADER_BYTES + runs * RUN_HEADER_BYTES + payload_cells(dst, runs) * CELL_BYTES);
    p
}

/// Sum of run lengths in an encoded payload — debug-assert support only.
#[cfg(debug_assertions)]
fn payload_cells(dst: &[u8], runs: usize) -> usize {
    let mut p = DELTA_HEADER_BYTES;
    let mut total = 0usize;
    for _ in 0..runs {
        let len = u16::from_le_bytes([dst[p + 2], dst[p + 3]]) as usize;
        total += len;
        p += RUN_HEADER_BYTES + len * CELL_BYTES;
    }
    total
}
#[cfg(not(debug_assertions))]
fn payload_cells(_dst: &[u8], _runs: usize) -> usize {
    0
}

/// Emit the KEYFRAME form: header (flags bit0) + one run `{start=0, len=1920}`
/// + the full 46080-byte packed grid (`KEYFRAME_BYTES`). The bootstrap /
/// full-frame / worst-case payload.
pub fn encode_keyframe(next: &[u8], dst: &mut [u8], epoch: u32) -> usize {
    put_header(dst, true, epoch, 1);
    let mut p = DELTA_HEADER_BYTES;
    dst[p..p + 2].copy_from_slice(&0u16.to_le_bytes()); // start 0
    dst[p + 2..p + 4].copy_from_slice(&(FRAME_CELLS as u16).to_le_bytes()); // len 1920
    p += RUN_HEADER_BYTES;
    dst[p..p + FRAME_BYTES].copy_from_slice(&next[..FRAME_BYTES]);
    p + FRAME_BYTES
}

/// Encode the delta, falling back to the keyframe form when the run-list would
/// meet or exceed the keyframe size (inclusive `>=`), bounding the worst case
/// at exactly `KEYFRAME_BYTES`. When `force_keyframe` is set (first send to a
/// slot, roster change, or a host rejection), the keyframe form is emitted
/// directly.
pub fn encode(prev: &[u8], next: &[u8], dst: &mut [u8], epoch: u32, force_keyframe: bool) -> usize {
    if force_keyframe {
        return encode_keyframe(next, dst, epoch);
    }
    let n = encode_run_list(prev, next, dst, epoch);
    if n >= KEYFRAME_BYTES {
        return encode_keyframe(next, dst, epoch);
    }
    n
}

/// Apply a delta to the packed baseline `prev` in place — the inverse of
/// `encode`, used by tests to prove the round-trip (the host applies deltas in
/// production). Returns false on a structurally malformed delta
/// (degrade-to-drop, never panics). A keyframe overwrites all cells.
#[cfg(test)]
pub(crate) fn apply(prev: &mut [u8], delta: &[u8]) -> bool {
    if delta.len() < DELTA_HEADER_BYTES || prev.len() < FRAME_BYTES {
        return false;
    }
    let flags = delta[0];
    if flags & !FLAG_KEYFRAME != 0 {
        return false; // unknown flag bits
    }
    if delta[7] != GEOMETRY_ROWS || delta[8] != GEOMETRY_COLS {
        return false; // wrong geometry
    }
    let run_count = u16::from_le_bytes([delta[5], delta[6]]) as usize;
    let mut p = DELTA_HEADER_BYTES;
    let mut last_end = 0usize;
    for _ in 0..run_count {
        if p + RUN_HEADER_BYTES > delta.len() {
            return false;
        }
        let start = u16::from_le_bytes([delta[p], delta[p + 1]]) as usize;
        let run_len = u16::from_le_bytes([delta[p + 2], delta[p + 3]]) as usize;
        p += RUN_HEADER_BYTES;
        if run_len == 0 || start < last_end || start + run_len > FRAME_CELLS {
            return false; // overlap / descending / out of bounds
        }
        let n = run_len * CELL_BYTES;
        if p + n > delta.len() {
            return false; // short read
        }
        let o = start * CELL_BYTES;
        prev[o..o + n].copy_from_slice(&delta[p..p + n]);
        p += n;
        last_end = start + run_len;
    }
    p == delta.len()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::frame::{Frame, Style, CYAN, WHITE, YELLOW};

    fn packed(build: impl FnOnce(&mut Frame)) -> Vec<u8> {
        let mut f = Frame::new();
        build(&mut f);
        let mut v = vec![0u8; FRAME_BYTES];
        f.pack_into(&mut v);
        v
    }

    #[test]
    fn cell_is_24_bytes_and_frame_is_46080() {
        assert_eq!(CELL_BYTES, 24);
        assert_eq!(FRAME_BYTES, 46080);
        assert_eq!(KEYFRAME_BYTES, 46093);
    }

    #[test]
    fn no_change_is_header_only() {
        let f = packed(|_| {});
        let mut dst = vec![0u8; KEYFRAME_BYTES];
        let n = encode_run_list(&f, &f, &mut dst, 7);
        assert_eq!(n, DELTA_HEADER_BYTES);
        assert_eq!(dst[0], 0); // not keyframe
        assert_eq!(u32::from_le_bytes([dst[1], dst[2], dst[3], dst[4]]), 7); // epoch
        assert_eq!(u16::from_le_bytes([dst[5], dst[6]]), 0); // runCount
        assert_eq!((dst[7], dst[8]), (24, 80)); // geometry
    }

    #[test]
    fn keyframe_layout_and_size() {
        let next = packed(|f| f.set_rune(2, 3, 'X', Style::new(WHITE, 0)));
        let mut dst = vec![0u8; KEYFRAME_BYTES];
        let n = encode_keyframe(&next, &mut dst, 9);
        assert_eq!(n, KEYFRAME_BYTES);
        assert_eq!(dst[0], FLAG_KEYFRAME);
        assert_eq!(u32::from_le_bytes([dst[1], dst[2], dst[3], dst[4]]), 9);
        assert_eq!(u16::from_le_bytes([dst[5], dst[6]]), 1); // runCount
        assert_eq!((dst[7], dst[8]), (24, 80));
        assert_eq!(u16::from_le_bytes([dst[9], dst[10]]), 0); // start
        assert_eq!(u16::from_le_bytes([dst[11], dst[12]]), FRAME_CELLS as u16); // len
        assert_eq!(&dst[13..], &next[..]);
    }

    #[test]
    fn round_trip_apply_equals_next() {
        let prev = packed(|f| {
            f.text(0, 0, "hello", Style::new(WHITE, 0));
        });
        let next = packed(|f| {
            f.text(0, 0, "hello", Style::new(WHITE, 0));
            f.set_rune(5, 10, 'Z', Style::new(CYAN, 0));
            f.text(12, 0, "world", Style::new(YELLOW, 0));
        });
        let mut dst = vec![0u8; KEYFRAME_BYTES];
        let n = encode_run_list(&prev, &next, &mut dst, 0);
        assert!(n < KEYFRAME_BYTES);
        let mut recon = prev.clone();
        assert!(apply(&mut recon, &dst[..n]));
        assert_eq!(recon, next);
    }

    #[test]
    fn full_change_falls_back_to_keyframe() {
        let prev = packed(|f| f.fill(0, 0, 23, 79, crate::frame::Cell { rune: 'A', ..Default::default() }));
        let next = packed(|f| f.fill(0, 0, 23, 79, crate::frame::Cell { rune: 'B', ..Default::default() }));
        let mut dst = vec![0u8; KEYFRAME_BYTES];
        let n = encode(&prev, &next, &mut dst, 0, false);
        assert_eq!(n, KEYFRAME_BYTES);
        assert_eq!(dst[0], FLAG_KEYFRAME);
    }

    #[test]
    fn malformed_deltas_are_rejected_not_panicking() {
        let mut prev = packed(|_| {});
        assert!(!apply(&mut prev, &[0u8; 3])); // truncated header
        let mut d = vec![0u8; DELTA_HEADER_BYTES];
        d[0] = 0x02; // unknown flag bit
        d[7] = 24;
        d[8] = 80;
        assert!(!apply(&mut prev, &d));
        let mut d = vec![0u8; DELTA_HEADER_BYTES];
        d[7] = 25; // wrong geometry
        d[8] = 80;
        assert!(!apply(&mut prev, &d));
        let mut d = vec![0u8; DELTA_HEADER_BYTES + RUN_HEADER_BYTES];
        d[5] = 1; // runCount = 1
        d[7] = 24;
        d[8] = 80;
        d[9..11].copy_from_slice(&1919u16.to_le_bytes()); // start
        d[11..13].copy_from_slice(&5u16.to_le_bytes()); // len overruns
        assert!(!apply(&mut prev, &d));
    }
}
