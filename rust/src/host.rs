//! Host transport — the crate's ONLY unsafe code, fenced by the crate-root
//! `#![deny(unsafe_code)]` and the module-level allow below.
//!
//! Under wasm: the extism kernel I/O plumbing (extism-pdk) plus the ten
//! shellcade host functions declared as RAW wasm imports (ABI.md §3, §7). The
//! PDK's `#[host_fn]` macro corrupts scalar args, so the host functions are
//! imported directly here.
//!
//! Under a native target (`cargo test`): a scriptable in-memory test host —
//! pure game/SDK logic runs natively with recorded effects, no Extism runtime.
#![allow(unsafe_code)]

// ---- wasm transport ----------------------------------------------------------

#[cfg(target_arch = "wasm32")]
mod imp {
    use extism_pdk::extism;
    use extism_pdk::Memory;

    #[link(wasm_import_module = "extism:host/user")]
    extern "C" {
        /// send(i64 playerIdx, ptr payload) → i64: deliver one delta container
        /// to one player; low 32 bits of the return carry the slot epoch.
        fn send(player_idx: u64, frame_off: u64) -> i64;
        /// identical(ptr payload) → i64: deliver one delta container to every
        /// player; low 32 bits carry the broadcast epoch.
        fn identical(frame_off: u64) -> i64;
        /// set_input_context(i64 ctx): 0 Nav, 1 Command, 2 Text.
        fn set_input_context(ctx: i64);
        /// end(ptr result): settle the room exactly once.
        fn end(result_off: u64);
        /// post(ptr result): record a leaderboard result without ending.
        fn post(result_off: u64);
        /// log(i64 level, ptr msg): 0 debug, 1 info, 2 warn, 3 error.
        fn log(level: i64, msg_off: u64);
        /// kv_get(i64 playerIdx, ptr key) → ptr (0 = not found).
        fn kv_get(player_idx: u64, key_off: u64) -> u64;
        /// kv_set(i64 playerIdx, ptr key, ptr val, ptr rule).
        fn kv_set(player_idx: u64, key_off: u64, val_off: u64, rule_off: u64);
        /// kv_delete(i64 playerIdx, ptr key).
        fn kv_delete(player_idx: u64, key_off: u64);
        /// config_get(ptr key) → ptr (0 = not found).
        fn config_get(key_off: u64) -> u64;
    }

    /// Stage pre-serialized bytes in extism kernel memory. Infallible by
    /// construction: `Memory::from_bytes` on a `&[u8]` cannot hit the fallible
    /// `ToBytes` path, and a kernel OOM traps host-side before returning.
    fn alloc(b: &[u8]) -> Memory {
        // SAFETY-adjacent note (no unsafe here): see module doc — &[u8] staging
        // is the infallible path.
        #[allow(clippy::expect_used)]
        Memory::from_bytes(b).expect("extism alloc")
    }

    fn read_free(off: u64) -> Option<Vec<u8>> {
        if off == 0 {
            return None;
        }
        let mem = Memory::find(off)?;
        let b = mem.to_vec();
        mem.free();
        Some(b)
    }

    pub fn read_input() -> Vec<u8> {
        // SAFETY: the extism kernel guarantees the input region is initialized
        // for the duration of the export call.
        unsafe { extism::load_input() }
    }

    pub fn write_output(b: &[u8]) {
        alloc(b).set_output();
    }

    pub fn host_send(player_idx: usize, payload: &[u8]) -> u32 {
        let mem = alloc(payload);
        // SAFETY: raw import per ABI.md §3; mem.offset() is a live kernel
        // allocation for the duration of the call.
        let ret = unsafe { send(player_idx as u64, mem.offset()) };
        mem.free();
        ret as u64 as u32 // low 32 bits = epoch; upper 32 reserved-zero
    }

    pub fn host_identical(payload: &[u8]) -> u32 {
        let mem = alloc(payload);
        // SAFETY: as host_send.
        let ret = unsafe { identical(mem.offset()) };
        mem.free();
        ret as u64 as u32
    }

    pub fn host_set_input_context(ctx: i64) {
        // SAFETY: scalar-only raw import per ABI.md §3.
        unsafe { set_input_context(ctx) };
    }

    pub fn host_end(result: &[u8]) {
        let mem = alloc(result);
        // SAFETY: as host_send.
        unsafe { end(mem.offset()) };
        mem.free();
    }

    pub fn host_post(result: &[u8]) {
        let mem = alloc(result);
        // SAFETY: as host_send.
        unsafe { post(mem.offset()) };
        mem.free();
    }

    pub fn host_log(level: i64, msg: &str) {
        let mem = alloc(msg.as_bytes());
        // SAFETY: as host_send.
        unsafe { log(level, mem.offset()) };
        mem.free();
    }

    pub fn host_kv_get(player_idx: usize, key: &str) -> Option<Vec<u8>> {
        let km = alloc(key.as_bytes());
        // SAFETY: as host_send; the returned offset (0 = not found) is a
        // host-allocated kernel region we read then free.
        let off = unsafe { kv_get(player_idx as u64, km.offset()) };
        km.free();
        read_free(off)
    }

    pub fn host_kv_set(player_idx: usize, key: &str, value: &[u8], rule: &str) {
        let km = alloc(key.as_bytes());
        let vm = alloc(value);
        let rm = alloc(rule.as_bytes());
        // SAFETY: as host_send.
        unsafe { kv_set(player_idx as u64, km.offset(), vm.offset(), rm.offset()) };
        km.free();
        vm.free();
        rm.free();
    }

    pub fn host_kv_delete(player_idx: usize, key: &str) {
        let km = alloc(key.as_bytes());
        // SAFETY: as host_send.
        unsafe { kv_delete(player_idx as u64, km.offset()) };
        km.free();
    }

    pub fn host_config_get(key: &str) -> Option<Vec<u8>> {
        let km = alloc(key.as_bytes());
        // SAFETY: as host_kv_get.
        let off = unsafe { config_get(km.offset()) };
        km.free();
        read_free(off)
    }
}

// ---- native test host (cargo test) ---------------------------------------------

#[cfg(not(target_arch = "wasm32"))]
mod imp {
    use std::cell::RefCell;
    use std::collections::{HashMap, VecDeque};

    /// One recorded `send`/`identical` call.
    #[derive(Clone, Debug)]
    pub struct SentPayload {
        /// `Some(idx)` for `send`, `None` for `identical`.
        pub player_idx: Option<usize>,
        pub payload: Vec<u8>,
    }

    /// The scriptable in-memory host double behind the native build. By default
    /// it ECHOES the epoch the guest stamped (always-accept); push onto
    /// `epoch_script` to simulate rejections (hibernation restore / baseline
    /// loss). Not public API — `#[doc(hidden)]` re-export for SDK and game
    /// tests.
    #[derive(Default)]
    pub struct TestHost {
        pub input: Vec<u8>,
        pub output: Vec<u8>,
        pub sent: Vec<SentPayload>,
        /// Scripted epoch returns, consumed per send/identical call; when empty
        /// the host echoes the payload's stamped epoch (bytes 1..5).
        pub epoch_script: VecDeque<u32>,
        pub input_contexts: Vec<i64>,
        pub ends: Vec<Vec<u8>>,
        pub posts: Vec<Vec<u8>>,
        pub logs: Vec<(i64, String)>,
        pub kv: HashMap<(usize, String), Vec<u8>>,
        pub config: HashMap<String, Vec<u8>>,
    }

    thread_local! {
        static HOST: RefCell<TestHost> = RefCell::new(TestHost::default());
    }

    /// Inspect/script the test host (native builds only).
    pub fn with_test_host<R>(f: impl FnOnce(&mut TestHost) -> R) -> R {
        HOST.with(|h| f(&mut h.borrow_mut()))
    }

    /// Reset the test host to defaults (fresh "instance").
    pub fn reset_test_host() {
        HOST.with(|h| *h.borrow_mut() = TestHost::default());
    }

    fn ret_epoch(h: &mut TestHost, payload: &[u8]) -> u32 {
        if let Some(e) = h.epoch_script.pop_front() {
            return e;
        }
        if payload.len() >= 5 {
            u32::from_le_bytes([payload[1], payload[2], payload[3], payload[4]])
        } else {
            0
        }
    }

    pub fn read_input() -> Vec<u8> {
        with_test_host(|h| h.input.clone())
    }
    pub fn write_output(b: &[u8]) {
        with_test_host(|h| h.output = b.to_vec());
    }
    pub fn host_send(player_idx: usize, payload: &[u8]) -> u32 {
        with_test_host(|h| {
            h.sent.push(SentPayload { player_idx: Some(player_idx), payload: payload.to_vec() });
            ret_epoch(h, payload)
        })
    }
    pub fn host_identical(payload: &[u8]) -> u32 {
        with_test_host(|h| {
            h.sent.push(SentPayload { player_idx: None, payload: payload.to_vec() });
            ret_epoch(h, payload)
        })
    }
    pub fn host_set_input_context(ctx: i64) {
        with_test_host(|h| h.input_contexts.push(ctx));
    }
    pub fn host_end(result: &[u8]) {
        with_test_host(|h| h.ends.push(result.to_vec()));
    }
    pub fn host_post(result: &[u8]) {
        with_test_host(|h| h.posts.push(result.to_vec()));
    }
    pub fn host_log(level: i64, msg: &str) {
        with_test_host(|h| h.logs.push((level, msg.to_string())));
    }
    pub fn host_kv_get(player_idx: usize, key: &str) -> Option<Vec<u8>> {
        with_test_host(|h| h.kv.get(&(player_idx, key.to_string())).cloned())
    }
    pub fn host_kv_set(player_idx: usize, key: &str, value: &[u8], _rule: &str) {
        with_test_host(|h| {
            h.kv.insert((player_idx, key.to_string()), value.to_vec());
        });
    }
    pub fn host_kv_delete(player_idx: usize, key: &str) {
        with_test_host(|h| {
            h.kv.remove(&(player_idx, key.to_string()));
        });
    }
    pub fn host_config_get(key: &str) -> Option<Vec<u8>> {
        with_test_host(|h| h.config.get(key).cloned())
    }
}

pub(crate) use imp::*;

// Native-only test-host handles, re-exported (doc-hidden) at the crate root so
// SDK unit tests and game crates can script the host without a wasm runtime.
#[cfg(not(target_arch = "wasm32"))]
pub use imp::{reset_test_host, with_test_host, SentPayload, TestHost};
