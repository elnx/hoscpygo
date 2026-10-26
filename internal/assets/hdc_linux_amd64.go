//go:build linux && amd64

package assets

import "embed"

const (
	hdcPlatformDir = "hdc/linux_amd64"
	hdcPlatformExe = "hdc"
	hdcPlatformLib = "libusb_shared.so"
)

//go:embed hdc/linux_amd64/hdc hdc/linux_amd64/libusb_shared.so
var hdcPlatformFS embed.FS
