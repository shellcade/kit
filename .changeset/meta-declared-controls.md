---
"kit": minor
---

Declared extra controls in `GameMeta` (wire revision 6): a new trailing
presence-guarded meta section listing inputs beyond the canonical control
vocabulary — a printable rune or a named key, each with a short display
label — so front ends on devices without the corresponding physical key
(touch) can surface each declaration as a tappable affordance that sends
exactly the declared input. Presentation metadata only: declarations change
no input interpretation, and games fully served by the canonical vocabulary
need none. Go SDK: `GameMeta.Controls` + `kit.RuneControl` /
`kit.KeyControl`; Rust SDK: `Meta::controls` + `ControlDecl`, encoded
byte-identically (golden-pinned). Validation (`wire.ValidateControls`,
enforced at `meta()` encode time): printable rune or assigned key code,
non-empty label ≤16 runes, no duplicate inputs, ≤32 declarations. ABI.md
§4.2 documents the section; GUIDE.md gains a mobile-friendly-controls
authoring section.
