// Package docs embeds the author-facing getting-started guide so the
// shellcade arcade can render it directly in its "Add your own game"
// screen — a glamour-rendered Markdown pane inside an 80x24 SSH TUI.
//
// Because of that 80x24 budget, get-started.md is authored terminal-first:
// prose is hard-wrapped at <=76 columns and avoids HTML, images, and wide
// tables. Keep it that way when editing — anything wider clips or wraps
// badly in the pager.
package docs

import _ "embed"

// GetStarted is the rendered-ready Markdown of the author getting-started
// guide (get-started.md), embedded at build time.
//
//go:embed get-started.md
var GetStarted string
