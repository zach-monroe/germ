package main

import "runtime/debug"

// version is set at release builds with -ldflags "-X main.version=v1.2.3".
var version = ""

// Version reports the build's version: the release tag, the module version
// for `go install ...@vX`, or "dev".
func Version() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}
