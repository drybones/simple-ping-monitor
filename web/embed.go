// Package web holds the browser UI, embedded into the binary so the page
// works with no internet connection.
package web

import "embed"

//go:embed static
var Static embed.FS
