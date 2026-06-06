//! The room PRNG: a small SplitMix64 generator seeded from the CallContext
//! seed, its state living in linear memory — hibernation reconstructs the
//! exact sequence (ABI.md §8 forbids ambient entropy). No `rand` dependency.

#[derive(Clone, Copy, Debug)]
pub(crate) struct SplitMix64 {
    state: u64,
}

impl SplitMix64 {
    pub fn new(seed: i64) -> Self {
        SplitMix64 { state: seed as u64 }
    }

    pub fn next_u64(&mut self) -> u64 {
        self.state = self.state.wrapping_add(0x9E3779B97F4A7C15);
        let mut z = self.state;
        z = (z ^ (z >> 30)).wrapping_mul(0xBF58476D1CE4E5B9);
        z = (z ^ (z >> 27)).wrapping_mul(0x94D049BB133111EB);
        z ^ (z >> 31)
    }

    /// Uniform value in `0..n` via rejection sampling; `0` when `n == 0`
    /// (defined, never panics).
    pub fn below(&mut self, n: u64) -> u64 {
        if n == 0 {
            return 0;
        }
        // Rejection zone keeps the distribution unbiased.
        let zone = u64::MAX - (u64::MAX % n);
        loop {
            let v = self.next_u64();
            if v < zone {
                return v % n;
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn deterministic_for_a_seed() {
        let mut a = SplitMix64::new(42);
        let mut b = SplitMix64::new(42);
        for _ in 0..100 {
            assert_eq!(a.next_u64(), b.next_u64());
        }
    }

    #[test]
    fn below_is_in_range_and_zero_safe() {
        let mut r = SplitMix64::new(7);
        assert_eq!(r.below(0), 0);
        for _ in 0..1000 {
            assert!(r.below(9) < 9);
        }
    }
}
