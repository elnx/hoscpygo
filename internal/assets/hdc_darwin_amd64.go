//go:build darwin && amd64

package assets

import "embed"

const (
	hdcPlatformDir = "hdc/darwin_amd64"
	hdcPlatformExe = "hdc"
	hdcPlatformLib = "libusb_shared.dylib"
)

//go:embed hdc/darwin_amd64/hdc hdc/darwin_amd64/libusb_shared.dylib
var hdcPlatformFS embed.FS
