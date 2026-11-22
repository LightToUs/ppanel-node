package core

import (
	"runtime/debug"
	"testing"
)

const (
	compatibleXrayPath       = "github.com/wyx2685/xray-core"
	compatibleXrayVersion    = "v0.0.0-20260930151924-e087e74a1bf2"
	compatibleRealityPath    = "github.com/xtls/reality"
	compatibleRealityVersion = "v0.0.0-20260322125925-9234c772ba8f"
)

func TestRealityDependenciesRemainMihomoCompatible(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("build information is unavailable")
	}

	assertReplacement := func(modulePath, replacementPath, replacementVersion string) {
		t.Helper()
		for _, dependency := range info.Deps {
			if dependency.Path != modulePath {
				continue
			}
			if dependency.Replace == nil {
				t.Fatalf("%s is not pinned to a compatible replacement", modulePath)
			}
			if dependency.Replace.Path != replacementPath ||
				dependency.Replace.Version != replacementVersion {
				t.Fatalf(
					"%s replacement = %s@%s, want %s@%s",
					modulePath,
					dependency.Replace.Path,
					dependency.Replace.Version,
					replacementPath,
					replacementVersion,
				)
			}
			return
		}
		t.Fatalf("%s is missing from build dependencies", modulePath)
	}

	assertReplacement(
		"github.com/xtls/xray-core",
		compatibleXrayPath,
		compatibleXrayVersion,
	)
	assertReplacement(
		"github.com/xtls/reality",
		compatibleRealityPath,
		compatibleRealityVersion,
	)
}
