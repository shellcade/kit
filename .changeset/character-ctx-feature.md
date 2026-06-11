---
"kit": minor
---

Per-member player character in the CallContext behind a new declared
`CtxFeatCharacter` (1<<1) ctx feature: each roster member carries
`str glyph · u8 ink RGB · u8 bg RGB · u8 asciiFallback` after its kind byte,
in both member-bearing forms (wire revision 5). Go and Rust SDKs expose
`Character` on `Player` and a `CharacterCell` / `character_cell` helper
returning the one styled cell — every catalogue glyph is width 1, so games
place a player's character with zero width logic. Non-declaring guests
decode byte-identically.
