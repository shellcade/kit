---
"kit": minor
---

Declared config key specs: `GameMeta.Config []ConfigKeySpec` (Go) /
`Meta.config: &[ConfigKeySpec]` (Rust) lets a game declare the admin-settable
config keys it reads — key, title, description, value type
(`text`/`number`/`bool`/`json`), the default used when unset, and (json keys)
an optional JSON Schema — so the arcade's admin tools can render typed get/edit
forms instead of a blind key/value prompt. Carried as a trailing
presence-guarded section of the packed Meta (ABI.md §4.2): old payloads decode
with no specs, old hosts ignore the trailing bytes — ABI major stays 2.
`wire.ValidateConfigSpecs` is the shared authoring rule set (unique non-empty
keys, no reserved `host.` prefix, schema only on json keys and well-formed),
enforced at `meta()` encode time by both SDKs; the Rust encoding is pinned
byte-identical to Go by a golden vector.
