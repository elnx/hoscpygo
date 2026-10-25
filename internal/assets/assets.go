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

//go:embed hdc
var hdcFS embed.FS

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
// current host. The caller extracts them to a directory because exec.Command
// needs real files.
func HDCBundle() (exeName string, exeData []byte, libName string, libData []byte, err error) {
	var dir, file, shared string
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		dir, file, shared = "linux_amd64", "hdc", "libusb_shared.so"
	case "darwin/amd64":
		dir, file, shared = "darwin_amd64", "hdc", "libusb_shared.dylib"
	case "darwin/arm64":
		dir, file, shared = "darwin_arm64", "hdc", "libusb_shared.dylib"
	case "windows/amd64":
		dir, file, shared = "windows_amd64", "hdc.exe", "libusb_shared.dll"
	default:
		return "", nil, "", nil, fmt.Errorf("no embedded hdc for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	exeData, err = hdcFS.ReadFile("hdc/" + dir + "/" + file)
	if err != nil {
		return "", nil, "", nil, err
	}
	libData, err = hdcFS.ReadFile("hdc/" + dir + "/" + shared)
	if err != nil {
		return "", nil, "", nil, err
	}
	return file, exeData, shared, libData, nil
}
