// Package assets embeds the web UI, device-side native assets and hdc binaries
// so that a single Go binary is self-contained.
package assets

import (
	"embed"
	"fmt"
	"io/fs"
	"runtime"
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

// HDCBundle returns the hdc executable and its companion shared library for the
// current build target. Each target only embeds its own hdc directory, so a
// cross-compiled binary does not carry the other platforms' hdc binaries.
// The caller extracts them to a directory because exec.Command needs real files.
func HDCBundle() (exeName string, exeData []byte, libName string, libData []byte, err error) {
	if hdcPlatformDir == "" {
		return "", nil, "", nil, fmt.Errorf("no embedded hdc for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	exeData, err = hdcPlatformFS.ReadFile(hdcPlatformDir + "/" + hdcPlatformExe)
	if err != nil {
		return "", nil, "", nil, err
	}
	libData, err = hdcPlatformFS.ReadFile(hdcPlatformDir + "/" + hdcPlatformLib)
	if err != nil {
		return "", nil, "", nil, err
	}
	return hdcPlatformExe, exeData, hdcPlatformLib, libData, nil
}
