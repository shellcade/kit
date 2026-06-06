---
"kit": patch
---

Fix Windows builds: the dev runner's terminal-resize watch (SIGWINCH) is now
behind Unix build tags with a no-op Windows fallback, so importing the kit
module compiles on GOOS=windows. The runner works on Windows minus live
re-letterboxing on resize (no SIGWINCH equivalent exists there).
