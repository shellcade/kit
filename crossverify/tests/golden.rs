//! Cross-language byte-identical verification: load the Go reference encoder's
//! golden vectors (`*.dgld`, emitted by `kit/internal/diffbench`
//! TestEmitGolden) and assert this Rust v2 encoder produces BYTE-IDENTICAL
//! output for every frame of every scenario.
//!
//! The golden dir comes from `DIFFBENCH_GOLDEN_DIR` (default `/tmp/dgld`). If no
//! `.dgld` files are present the test is skipped with a clear message (so the
//! crate's `cargo test` never fails just because the goldens weren't emitted),
//! but the run prints the per-scenario and total frame counts and mismatches.
//!
//! File format (little-endian), magic "DGLD" v2. The INPUT frame is a
//! changed-cell list vs the previous frame (an encoder-independent format, so
//! verification is not circular); applying it to `prev` yields `next`.
//!
//!   "DGLD" | u32 version=2 | u32 frameBytes=46080 | u32 frameCount
//!   per frame:
//!     u32 changedCount | changedCount * (u16 cellIndex + 24 packed bytes)  (INPUT delta vs prev)
//!     u32 runlistLen   | run-list bytes
//!     u32 fallbackLen  | run-or-keyframe bytes
//!
//! The keyframe golden is not stored; the expected keyframe bytes are
//! reconstructed here from `next` (header + one full run) and the Rust
//! encoder's output is asserted byte-identical against that reconstruction.
//!
//! prev for frame 0 is a BLANK frame (all-space cells); thereafter prev = the
//! previous frame's reconstructed input.

use std::fs;
use std::path::PathBuf;

use diff_rs::{
    encode_keyframe, encode_run_list, encode_run_list_or_keyframe, CELL_BYTES, DELTA_HEADER_BYTES,
    FLAG_KEYFRAME, FRAME_BYTES, FRAME_CELLS, KEYFRAME_BYTES, MAX_ENCODED, RUN_HEADER_BYTES,
};

struct Rd<'a> {
    b: &'a [u8],
    off: usize,
}

impl<'a> Rd<'a> {
    fn u16(&mut self) -> u16 {
        let v = u16::from_le_bytes([self.b[self.off], self.b[self.off + 1]]);
        self.off += 2;
        v
    }
    fn u32(&mut self) -> u32 {
        let v = u32::from_le_bytes([
            self.b[self.off],
            self.b[self.off + 1],
            self.b[self.off + 2],
            self.b[self.off + 3],
        ]);
        self.off += 4;
        v
    }
    fn bytes(&mut self, n: usize) -> &'a [u8] {
        let s = &self.b[self.off..self.off + n];
        self.off += n;
        s
    }
    fn blob(&mut self) -> &'a [u8] {
        let n = self.u32() as usize;
        let s = &self.b[self.off..self.off + n];
        self.off += n;
        s
    }
}

fn blank_frame() -> Vec<u8> {
    let mut f = vec![0u8; FRAME_BYTES];
    for i in 0..FRAME_CELLS {
        f[i * CELL_BYTES..i * CELL_BYTES + 4].copy_from_slice(&(b' ' as u32).to_le_bytes());
    }
    f
}

fn golden_dir() -> PathBuf {
    if let Ok(d) = std::env::var("DIFFBENCH_GOLDEN_DIR") {
        return PathBuf::from(d);
    }
    // Default: the committed golden vectors checked in alongside this crate, so
    // `cargo test` is self-contained (no need to run the Go harness first).
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/golden")
}

#[test]
fn byte_identical_against_go_reference() {
    let dir = golden_dir();
    let mut files: Vec<PathBuf> = match fs::read_dir(&dir) {
        Ok(rd) => rd
            .filter_map(|e| e.ok().map(|e| e.path()))
            .filter(|p| p.extension().map(|x| x == "dgld").unwrap_or(false))
            .collect(),
        Err(_) => Vec::new(),
    };
    files.sort();

    if files.is_empty() {
        eprintln!(
            "SKIP: no *.dgld golden vectors in {} — emit them with:\n  \
             (cd kit && DIFFBENCH_GOLDEN_DIR={} go test -run TestEmitGolden ./internal/diffbench/)",
            dir.display(),
            dir.display()
        );
        return;
    }

    let mut dst = vec![0u8; MAX_ENCODED];
    let mut total_frames = 0usize;
    let mut total_mismatch = 0usize;
    let mut scenarios = 0usize;

    for path in &files {
        let bytes = fs::read(path).expect("read golden");
        assert_eq!(&bytes[0..4], b"DGLD", "{}: bad magic", path.display());
        let mut r = Rd { b: &bytes, off: 4 };
        let version = r.u32();
        assert_eq!(version, 2, "{}: bad version", path.display());
        let frame_bytes = r.u32() as usize;
        assert_eq!(frame_bytes, FRAME_BYTES, "{}: frameBytes", path.display());
        let frame_count = r.u32() as usize;

        let name = path.file_stem().unwrap().to_string_lossy().to_string();
        let mut prev = blank_frame();
        let mut scenario_mismatch = 0usize;

        for fi in 0..frame_count {
            // Reconstruct `next` by applying the INPUT changed-cell delta to prev.
            let mut next = prev.clone();
            let changed = r.u32() as usize;
            for _ in 0..changed {
                let idx = r.u16() as usize;
                let cell = r.bytes(CELL_BYTES);
                next[idx * CELL_BYTES..idx * CELL_BYTES + CELL_BYTES].copy_from_slice(cell);
            }
            let _ = fi;
            let g_runlist = r.blob();
            let g_fallback = r.blob();

            let n = encode_run_list(&prev, &next, &mut dst);
            if &dst[..n] != g_runlist {
                scenario_mismatch += 1;
                report_mismatch(&name, fi, "RUN-LIST", &dst[..n], g_runlist);
            }

            // Reconstruct the expected keyframe bytes independently (header with
            // bit0 + one run start=0 len=1920 + full next), then assert the Rust
            // keyframe encoder is byte-identical to it.
            let mut expect_kf = vec![0u8; KEYFRAME_BYTES];
            expect_kf[0] = FLAG_KEYFRAME;
            expect_kf[5..7].copy_from_slice(&1u16.to_le_bytes()); // runCount = 1
            expect_kf[7] = 24; // rows geometry byte
            expect_kf[8] = 80; // cols geometry byte
            expect_kf[DELTA_HEADER_BYTES..DELTA_HEADER_BYTES + 2]
                .copy_from_slice(&0u16.to_le_bytes()); // start 0
            expect_kf[DELTA_HEADER_BYTES + 2..DELTA_HEADER_BYTES + 4]
                .copy_from_slice(&(FRAME_CELLS as u16).to_le_bytes()); // len 1920
            expect_kf[DELTA_HEADER_BYTES + RUN_HEADER_BYTES..].copy_from_slice(&next);
            let n = encode_keyframe(&prev, &next, &mut dst);
            if dst[..n] != expect_kf[..] {
                scenario_mismatch += 1;
                report_mismatch(&name, fi, "KEYFRAME", &dst[..n], &expect_kf);
            }

            let n = encode_run_list_or_keyframe(&prev, &next, &mut dst);
            if &dst[..n] != g_fallback {
                scenario_mismatch += 1;
                report_mismatch(&name, fi, "RUN+KEYFRAME-fallback", &dst[..n], g_fallback);
            }

            total_frames += 1;
            prev = next;
        }

        scenarios += 1;
        total_mismatch += scenario_mismatch;
        eprintln!(
            "{:<28} {:>5} frames  mismatches: {}",
            name, frame_count, scenario_mismatch
        );
    }

    eprintln!(
        "\nTOTAL: {} scenarios, {} frames compared (x3 encoders = {} comparisons), {} mismatches",
        scenarios,
        total_frames,
        total_frames * 3,
        total_mismatch
    );
    assert_eq!(total_mismatch, 0, "byte-identical verification FAILED");
}

fn report_mismatch(scenario: &str, frame: usize, enc: &str, got: &[u8], want: &[u8]) {
    let first = (0..got.len().min(want.len()))
        .find(|&i| got[i] != want[i])
        .map(|i| i as i64)
        .unwrap_or(-1);
    eprintln!(
        "MISMATCH {scenario} frame {frame} {enc}: got {} B want {} B, first diff @ {first}",
        got.len(),
        want.len()
    );
}
