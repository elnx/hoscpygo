//go:build darwin && arm64

package assets

import "embed"

const (
	hdcPlatformDir = "hdc/darwin_arm64"
	hdcPlatformExe = "hdc"
	hdcPlatformLib = "libusb_shared.dylib"
)

//go:embed hdc/darwin_arm64/hdc hdc/darwin_arm64/libusb_shared.dylib
var hdcPlatformFS embed.FS
