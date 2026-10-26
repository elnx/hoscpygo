package assets

import (
	"runtime"
	"testing"
)

func TestHDCBundleForBuildTarget(t *testing.T) {
	exe, data, lib, libData, err := HDCBundle()
	if err != nil {
		t.Skipf("no embedded hdc for %s/%s: %v", runtime.GOOS, runtime.GOARCH, err)
	}
	if exe == "" || lib == "" || len(data) == 0 || len(libData) == 0 {
		t.Fatalf("incomplete embedded hdc: exe=%q data=%d lib=%q libData=%d", exe, len(data), lib, len(libData))
	}
}
