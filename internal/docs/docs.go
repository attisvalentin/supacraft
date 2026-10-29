// Package docs embeds the supacraft.json reference served to AI agents.
package docs

import _ "embed"

// Reference is the full supacraft.json format reference (Markdown).
//
//go:embed reference.md
var Reference string
