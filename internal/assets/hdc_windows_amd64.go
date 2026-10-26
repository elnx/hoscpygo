//go:build windows && amd64

package assets

import "embed"

const (
	hdcPlatformDir = "hdc/windows_amd64"
	hdcPlatformExe = "hdc.exe"
	hdcPlatformLib = "libusb_shared.dll"
)

//go:embed hdc/windows_amd64/hdc.exe hdc/windows_amd64/libusb_shared.dll
var hdcPlatformFS embed.FS
