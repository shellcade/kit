//! The fixed 80x24 cell grid and its packed wire encoding (ABI.md §4.3, v2
//! 24-byte grapheme cell), mirroring the Go SDK's authoring Frame. Writes
//! outside the grid are clamped (never errored, never panicking). A composed
//! Frame packs to exactly 46080 bytes.
//!
//! v2 cell anchor layout (24 bytes, little-endian):
//!   u32 rune @0 | u32 cp2 @4 | u32 cp3 @8        base + extra grapheme code points
//!   u8 fgSet,fgR,fgG,fgB @12 | u8 bgSet,bgR,bgG,bgB @16
//!   u8 attr @20 | u8 cont @21 | u16 pad @22 (zero)
//!
//! Canonical-zero rule: unused cp slots and pad MUST be zero, so cell equality
//! is exactly a 24-byte memcmp (load-bearing for the delta diff and hibernation
//! byte-identity). `pack_into` is the normative enforcer — it always writes
//! pad = 0, and an unset color always packs as four zero bytes.

use crate::types::Character;

/// Grid height in rows. Signed so coordinate math (centering, right-aligning)
/// never fights sign conversions; negative intermediate columns are legal and
/// clamp away.
pub const ROWS: i32 = 24;
/// Grid width in columns (see [`ROWS`] for why this is signed).
pub const COLS: i32 = 80;

/// v2 grapheme cell width on the wire.
pub(crate) const CELL_BYTES: usize = 24;
pub(crate) const FRAME_CELLS: usize = (ROWS as usize) * (COLS as usize); // 1920
pub(crate) const FRAME_BYTES: usize = FRAME_CELLS * CELL_BYTES; // 46080

/// An optional truecolor value; the default/unset value maps to the terminal
/// default. Construct with [`Color::rgb`] or [`Color::gray`].
#[derive(Clone, Copy, PartialEq, Eq, Default, Debug)]
pub struct Color {
    set: bool,
    r: u8,
    g: u8,
    b: u8,
}

impl Color {
    /// The terminal-default (unset) color — what `Color::default()` also yields.
    pub const fn unset() -> Self {
        Color { set: false, r: 0, g: 0, b: 0 }
    }
    /// A truecolor value.
    pub const fn rgb(r: u8, g: u8, b: u8) -> Self {
        Color { set: true, r, g, b }
    }
    /// An even gray.
    pub const fn gray(v: u8) -> Self {
        Color::rgb(v, v, v)
    }
    /// Whether the color is set (vs terminal default).
    pub const fn is_set(self) -> bool {
        self.set
    }
    /// The color components.
    pub const fn rgb_vals(self) -> (u8, u8, u8) {
        (self.r, self.g, self.b)
    }
}

// Standard palette (matches the Go SDK / platform canvas constants).
pub const WHITE: Color = Color::rgb(0xff, 0xff, 0xff);
pub const RED: Color = Color::rgb(0xff, 0x55, 0x55);
pub const GREEN: Color = Color::rgb(0x55, 0xff, 0x55);
pub const YELLOW: Color = Color::rgb(0xff, 0xff, 0x55);
pub const CYAN: Color = Color::rgb(0x55, 0xff, 0xff);
pub const DIM_GRAY: Color = Color::gray(0x6c);

// Attribute bits (ABI.md §4.3).
pub const ATTR_BOLD: u8 = 1 << 0;
pub const ATTR_DIM: u8 = 1 << 1;
pub const ATTR_UNDERLINE: u8 = 1 << 2;
pub const ATTR_REVERSE: u8 = 1 << 3;

/// Style bundles the styling applied when writing text.
#[derive(Clone, Copy, Default, Debug)]
pub struct Style {
    pub fg: Color,
    pub bg: Color,
    pub attr: u8,
}

impl Style {
    /// Foreground-and-attributes style (background stays terminal default).
    pub const fn new(fg: Color, attr: u8) -> Self {
        Style { fg, bg: Color::unset(), attr }
    }
}

/// One drawable cell. `rune` is the base code point; `cp2`/`cp3` carry the
/// extra code points of a grapheme cluster (VS16, skin-tone modifier, keycap
/// U+20E3), `'\0'` = unused. Single-code-point authoring leaves them `'\0'`
/// by default, so `set_rune`/`set`/`text`/`set_wide` are unchanged.
#[derive(Clone, Copy, Debug)]
pub struct Cell {
    pub rune: char,
    pub cp2: char,
    pub cp3: char,
    pub fg: Color,
    pub bg: Color,
    pub attr: u8,
    pub cont: bool,
}

impl Cell {
    pub(crate) const fn blank() -> Self {
        Cell {
            rune: ' ',
            cp2: '\0',
            cp3: '\0',
            fg: Color::unset(),
            bg: Color::unset(),
            attr: 0,
            cont: false,
        }
    }
}

impl Default for Cell {
    /// A blank cell (space, default colors) — the state every frame starts in.
    fn default() -> Self {
        Cell::blank()
    }
}

/// The fixed 24x80 grid a game composes and sends. Frames live on the heap
/// (~46 KB packed) — keep one in your handler and [`Frame::clear`] it each
/// render for the allocation-free steady state.
pub struct Frame {
    cells: Vec<Cell>, // FRAME_CELLS, row-major
}

impl Frame {
    /// A grid filled with blank cells.
    pub fn new() -> Self {
        Frame { cells: vec![Cell::blank(); FRAME_CELLS] }
    }

    /// Reset every cell to a blank (space, default colors), so one Frame can be
    /// reused across renders — the allocation-free steady state.
    pub fn clear(&mut self) {
        self.cells.fill(Cell::blank());
    }

    fn idx(row: i32, col: i32) -> Option<usize> {
        if row >= 0 && row < ROWS && col >= 0 && col < COLS {
            Some((row as usize) * (COLS as usize) + (col as usize))
        } else {
            None
        }
    }

    /// Write one cell; out-of-bounds writes are clamped (dropped).
    pub fn set(&mut self, row: i32, col: i32, cell: Cell) {
        if let Some(i) = Self::idx(row, col) {
            self.cells[i] = cell;
        }
    }

    /// Read one cell; out-of-bounds reads return a blank.
    pub fn get(&self, row: i32, col: i32) -> Cell {
        Self::idx(row, col).map_or(Cell::blank(), |i| self.cells[i])
    }

    /// Write one styled rune; out-of-bounds writes are clamped (dropped).
    pub fn set_rune(&mut self, row: i32, col: i32, r: char, st: Style) {
        self.set(row, col, Cell { rune: r, fg: st.fg, bg: st.bg, attr: st.attr, ..Cell::blank() });
    }

    /// Write a double-width rune: the glyph occupies `(row, col)` and its
    /// continuation cell `(row, col+1)`, marked `cont` so the renderer skips it.
    /// The whole write is REFUSED (nothing drawn, `col` returned unchanged) when
    /// out of bounds or when the continuation cell would fall off the right edge
    /// — a half-glyph would desync every column to its right. On success returns
    /// `col + 2`.
    pub fn set_wide(&mut self, row: i32, col: i32, r: char, st: Style) -> i32 {
        if Self::idx(row, col).is_none() || col + 1 >= COLS {
            return col;
        }
        self.set_rune(row, col, r, st);
        self.set(row, col + 1, Cell { rune: '\0', fg: st.fg, bg: st.bg, attr: st.attr, cont: true, ..Cell::blank() });
        col + 2
    }

    /// Write a grapheme cluster of up to three code points into one cell (VS16
    /// emoji, skin-tone-modified emoji, keycaps like base + U+20E3). REFUSES a
    /// cluster of zero or more than three code points (e.g. a family ZWJ emoji):
    /// draws nothing and returns `col` unchanged — refusing rather than
    /// truncating to a different, valid-looking glyph. On success returns
    /// `col + 1`. The SDK never measures display width; width-1 is the author's
    /// contract here.
    pub fn set_grapheme(&mut self, row: i32, col: i32, cluster: &str, st: Style) -> i32 {
        if Self::idx(row, col).is_none() {
            return col;
        }
        let Some((base, cp2, cp3)) = decode_cluster(cluster) else {
            return col;
        };
        self.set(row, col, Cell { rune: base, cp2, cp3, fg: st.fg, bg: st.bg, attr: st.attr, cont: false });
        col + 1
    }

    /// The width-2 companion of [`Frame::set_grapheme`], mirroring
    /// [`Frame::set_wide`]: refuses an over-/zero-length cluster and refuses at
    /// the right edge, dropping rather than drawing a half-glyph. On success
    /// returns `col + 2`.
    pub fn set_grapheme_wide(&mut self, row: i32, col: i32, cluster: &str, st: Style) -> i32 {
        if Self::idx(row, col).is_none() || col + 1 >= COLS {
            return col;
        }
        let Some((base, cp2, cp3)) = decode_cluster(cluster) else {
            return col;
        };
        self.set(row, col, Cell { rune: base, cp2, cp3, fg: st.fg, bg: st.bg, attr: st.attr, cont: false });
        self.set(row, col + 1, Cell { rune: '\0', fg: st.fg, bg: st.bg, attr: st.attr, cont: true, ..Cell::blank() });
        col + 2
    }

    /// Write a string left-to-right, clamped to the row. Returns the next col.
    pub fn text(&mut self, row: i32, col: i32, s: &str, st: Style) -> i32 {
        let mut c = col;
        for ch in s.chars() {
            self.set_rune(row, c, ch, st);
            c += 1;
        }
        c
    }

    /// Write a string so it ends at col `end` (inclusive).
    pub fn text_right(&mut self, row: i32, end: i32, s: &str, st: Style) {
        let n = s.chars().count() as i32;
        self.text(row, end - n + 1, s, st);
    }

    /// Paint a rectangle (inclusive bounds) with the given cell.
    pub fn fill(&mut self, r0: i32, c0: i32, r1: i32, c1: i32, cell: Cell) {
        for r in r0..=r1 {
            for c in c0..=c1 {
                self.set(r, c, cell);
            }
        }
    }

    /// Pack the frame into `dst` (must be `FRAME_BYTES`), per the ABI.md §4.3
    /// v2 anchor layout. This is the canonical-zero enforcer: pad is always
    /// written zero and an unset color packs as four zero bytes, regardless of
    /// the in-memory Cell.
    pub(crate) fn pack_into(&self, dst: &mut [u8]) {
        debug_assert!(dst.len() >= FRAME_BYTES);
        for (i, cell) in self.cells.iter().enumerate() {
            let o = i * CELL_BYTES;
            dst[o..o + 4].copy_from_slice(&(cell.rune as u32).to_le_bytes());
            dst[o + 4..o + 8].copy_from_slice(&(cell.cp2 as u32).to_le_bytes());
            dst[o + 8..o + 12].copy_from_slice(&(cell.cp3 as u32).to_le_bytes());
            pack_color(&mut dst[o + 12..o + 16], cell.fg);
            pack_color(&mut dst[o + 16..o + 20], cell.bg);
            dst[o + 20] = cell.attr;
            dst[o + 21] = u8::from(cell.cont);
            dst[o + 22] = 0; // pad (canonical zero)
            dst[o + 23] = 0;
        }
    }
}

impl Default for Frame {
    fn default() -> Self {
        Frame::new()
    }
}

/// The one ready-made cell of a member's character tile: the glyph styled
/// with the resolved ink and background (player-character capability,
/// shellcade — every catalogue glyph is width 1, so games place a character
/// with zero width logic). The default [`Character`] (the game's meta does
/// not declare [`CTX_FEAT_CHARACTER`]) yields a blank cell.
///
/// [`CTX_FEAT_CHARACTER`]: crate::types::CTX_FEAT_CHARACTER
pub fn character_cell(c: &Character) -> Cell {
    let Some(rune) = c.glyph.chars().next() else {
        return Cell::blank();
    };
    Cell {
        rune,
        fg: Color::rgb(c.ink_r, c.ink_g, c.ink_b),
        bg: Color::rgb(c.bg_r, c.bg_g, c.bg_b),
        ..Cell::blank()
    }
}

fn pack_color(dst: &mut [u8], c: Color) {
    if c.is_set() {
        let (r, g, b) = c.rgb_vals();
        dst[0] = 1;
        dst[1] = r;
        dst[2] = g;
        dst[3] = b;
    } else {
        dst[0] = 0;
        dst[1] = 0;
        dst[2] = 0;
        dst[3] = 0;
    }
}

/// Decode a cluster into up to three code points; `None` when the cluster has
/// zero code points or more than three (the unsupported case).
fn decode_cluster(cluster: &str) -> Option<(char, char, char)> {
    let mut it = cluster.chars();
    let base = it.next()?;
    let cp2 = it.next().unwrap_or('\0');
    let cp3 = it.next().unwrap_or('\0');
    if it.next().is_some() {
        return None; // >3 code points: unsupported
    }
    Some((base, cp2, cp3))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pack_is_canonical_zero_even_with_dirty_cells() {
        let mut f = Frame::new();
        // A dirty in-memory cell: unset colors but nonzero rgb is impossible via
        // the constructors (fields are private), so the canonical-zero risk is
        // pad bytes — pack_into always writes them zero.
        f.set_rune(0, 0, 'A', Style::new(WHITE, ATTR_BOLD));
        let mut p = vec![0xAAu8; FRAME_BYTES]; // poisoned dst: every byte must be written
        f.pack_into(&mut p);
        for i in 0..FRAME_CELLS {
            let o = i * CELL_BYTES;
            assert_eq!(&p[o + 22..o + 24], &[0, 0], "pad must be zero at cell {i}");
        }
        // unset colors pack as four zero bytes
        let o = CELL_BYTES; // cell 1 is blank
        assert_eq!(&p[o + 12..o + 20], &[0u8; 8]);
    }

    #[test]
    fn oob_writes_clamp_and_never_panic() {
        let mut f = Frame::new();
        f.set_rune(-1, 0, 'x', Style::default());
        f.set_rune(0, -5, 'x', Style::default());
        f.set_rune(ROWS, 0, 'x', Style::default());
        f.set_rune(0, COLS, 'x', Style::default());
        assert_eq!(f.text(0, -2, "abcd", Style::default()), 2); // clamps, advances
        assert_eq!(f.get(0, 0).rune, 'c');
        assert_eq!(f.get(0, 1).rune, 'd');
    }

    #[test]
    fn wide_refuses_at_right_edge() {
        let mut f = Frame::new();
        assert_eq!(f.set_wide(0, COLS - 1, '日', Style::default()), COLS - 1);
        assert_eq!(f.get(0, COLS - 1).rune, ' '); // untouched
        assert_eq!(f.set_wide(0, 0, '日', Style::default()), 2);
        assert!(f.get(0, 1).cont);
    }

    #[test]
    fn grapheme_accepts_up_to_three_refuses_more() {
        let mut f = Frame::new();
        // keycap: '7' + U+FE0F + U+20E3 (3 code points)
        assert_eq!(f.set_grapheme(1, 1, "7\u{FE0F}\u{20E3}", Style::default()), 2);
        let c = f.get(1, 1);
        assert_eq!((c.rune, c.cp2, c.cp3), ('7', '\u{FE0F}', '\u{20E3}'));
        // family ZWJ emoji (>3): refused
        assert_eq!(f.set_grapheme(1, 5, "👨\u{200D}👩\u{200D}👧", Style::default()), 5);
        assert_eq!(f.get(1, 5).rune, ' ');
        // empty: refused
        assert_eq!(f.set_grapheme(1, 6, "", Style::default()), 6);
    }

    #[test]
    fn text_right_ends_at_inclusive_col() {
        let mut f = Frame::new();
        f.text_right(2, COLS - 1, "hi", Style::default());
        assert_eq!(f.get(2, COLS - 2).rune, 'h');
        assert_eq!(f.get(2, COLS - 1).rune, 'i');
    }

    /// character_cell turns a character into one styled, ready-to-place cell;
    /// the default Character (feature not declared) yields a blank.
    #[test]
    fn character_cell_styles_the_glyph() {
        let c = Character {
            glyph: "λ".into(),
            ink_r: 0x39,
            ink_g: 0xFF,
            ink_b: 0x14,
            bg_r: 0x2D,
            bg_g: 0x1B,
            bg_b: 0x4E,
            fallback: b'L',
        };
        let cell = character_cell(&c);
        assert_eq!(cell.rune, 'λ');
        assert_eq!(cell.fg, Color::rgb(0x39, 0xFF, 0x14));
        assert_eq!(cell.bg, Color::rgb(0x2D, 0x1B, 0x4E));
        assert_eq!((cell.cp2, cell.cp3), ('\0', '\0'));
        assert_eq!(cell.attr, 0);
        assert!(!cell.cont);
    }

    #[test]
    fn character_cell_default_is_blank() {
        let blank = character_cell(&Character::default());
        assert_eq!(blank.rune, ' ');
        assert!(!blank.fg.is_set());
        assert!(!blank.bg.is_set());
        assert_eq!((blank.cp2, blank.cp3), ('\0', '\0'));
        assert_eq!(blank.attr, 0);
        assert!(!blank.cont);
    }

    #[test]
    fn clear_resets_to_blank() {
        let mut f = Frame::new();
        f.fill(0, 0, ROWS - 1, COLS - 1, Cell { rune: '#', ..Cell::blank() });
        f.clear();
        let mut a = vec![0u8; FRAME_BYTES];
        let mut b = vec![0u8; FRAME_BYTES];
        f.pack_into(&mut a);
        Frame::new().pack_into(&mut b);
        assert_eq!(a, b);
    }
}
