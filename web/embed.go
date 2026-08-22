// Package web holds the frontend, embedded into the binary with no build step.
// The scripts are ES modules served straight from js/ — the browser resolves
// the imports, so there is still nothing to compile, bundle or install.
package web

import "embed"

//go:embed index.html styles.css js
var Files embed.FS
