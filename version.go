package main

import "runtime/debug"

// version can be set by packagers with -ldflags "-X main.version=v1.2.3".
// Otherwise it comes from the build info Go embeds: the module version for
// `go install …@vX.Y.Z`, or a VCS-derived version for a local build.
var version = ""

func versionNumber() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}

// versionString is what --version prints: tidefiles' name when started under
// it, since old copies' updaters check for that (rename.go).
func versionString() string {
	if invokedAsOld() {
		return oldName + " " + versionNumber()
	}
	return appName + " " + versionNumber()
}
