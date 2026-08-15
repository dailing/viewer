// Package web exposes the frontend distribution embedded in viewerd.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// Dist returns an fs.FS rooted at the embedded frontend dist directory.
func Dist() (fs.FS, error) { return fs.Sub(embedded, "dist") }
