//! Rough CPU sanity timing for the v2 RUN-LIST encoder, the Rust counterpart to
//! the Go `BenchmarkEncode` ns/op. No criterion: a plain warm-loop timer over a
//! handful of representative change densities, reporting ns/frame.
//!
//!   rustup run stable cargo run --release --example bench
//!
//! (release profile is mandatory for a meaningful number; debug is ~10-30x
//! slower and not representative of what the wasm guest's optimizer emits.)

use std::time::Instant;

use diff_rs::{encode_run_list, CELL_BYTES, FRAME_BYTES, FRAME_CELLS, MAX_ENCODED};

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

/// Time `iters` encodes of (prev -> next) and return ns per encode.
fn time_encode(prev: &[u8], next: &[u8], iters: u64) -> f64 {
    let mut dst = vec![0u8; MAX_ENCODED];
    // warm
    for _ in 0..1000 {
        std::hint::black_box(encode_run_list(prev, next, &mut dst));
    }
    let t0 = Instant::now();
    let mut sink = 0usize;
    for _ in 0..iters {
        sink = sink.wrapping_add(encode_run_list(
            std::hint::black_box(prev),
            std::hint::black_box(next),
            &mut dst,
        ));
    }
    std::hint::black_box(sink);
    t0.elapsed().as_nanos() as f64 / iters as f64
}

fn main() {
    let iters: u64 = 200_000;
    let blank = blank_frame();

    // no-change (header-only delta): the static-idle / coalesce-skip hot case.
    let no_change = blank.clone();
    println!(
        "no-change        (0 cells)  {:>8.1} ns/frame",
        time_encode(&blank, &no_change, iters)
    );

    // single-cell change (cursor blink): best delta case.
    let mut one = blank.clone();
    put_rune(&mut one, 12 * 80 + 23, b'_' as u32);
    println!(
        "single-cell      (1 cell)   {:>8.1} ns/frame",
        time_encode(&blank, &one, iters)
    );

    // one full row (marquee / scroll): 80 contiguous changed cells.
    let mut row = blank.clone();
    for c in 0..80 {
        put_rune(&mut row, 23 * 80 + c, b'#' as u32);
    }
    println!(
        "one-row          (80 cells) {:>8.1} ns/frame",
        time_encode(&blank, &row, iters)
    );

    // full-change (worst case): every cell differs, one giant run.
    let mut full_a = blank_frame();
    let mut full_b = blank_frame();
    for i in 0..FRAME_CELLS {
        put_rune(&mut full_a, i, b'A' as u32);
        put_rune(&mut full_b, i, b'B' as u32);
    }
    println!(
        "full-change      (1920)     {:>8.1} ns/frame",
        time_encode(&full_a, &full_b, iters / 4)
    );
}
