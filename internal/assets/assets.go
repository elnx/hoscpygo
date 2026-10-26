// Package assets embeds the web UI and device-side native assets so that the
// application binary is self-contained. hdc is an external host prerequisite.
package assets

import (
	"embed"
	"io/fs"
)

//go:embed web
var webFS embed.FS

//go:embed device
var deviceFS embed.FS

// Web returns the embedded web UI rooted at the web directory.
func Web() fs.FS {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	return sub
}

// Device returns an embedded device-side asset by its base name.
func Device(name string) ([]byte, error) {
	return deviceFS.ReadFile("device/" + name)
}
