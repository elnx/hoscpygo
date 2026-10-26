//go:build !(linux && amd64) && !(darwin && amd64) && !(darwin && arm64) && !(windows && amd64)

package assets

import "embed"

// No hdc bundle is embedded for this target. hoscpygo still runs if a system
// hdc is available; only the self-contained fallback is unavailable.
const (
	hdcPlatformDir = ""
	hdcPlatformExe = ""
	hdcPlatformLib = ""
)

var hdcPlatformFS embed.FS
