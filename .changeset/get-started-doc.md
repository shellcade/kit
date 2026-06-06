---
"kit": minor
---

docs: export the getting-started guide via the new `docs` package. A new
author-facing `docs/get-started.md` (terminal-first, hard-wrapped at <=76
columns) covers the quickstart (`shellcade-kit new` / `check` / `play`),
publishing to the public games catalog, and linking GitHub over SSH. The
new `docs` package embeds it as `docs.GetStarted` so the shellcade arcade
can render it directly in its "Add your own game" screen.
