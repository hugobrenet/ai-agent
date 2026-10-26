// Package buildinfo gives the version of the agent binary.
package buildinfo

import "runtime/debug"

// version is the version an image build sets with -ldflags -X, from git
// describe: a build without the git directory records no revision.
var version string

// Version is the version of the binary: the one its build set, else from its
// build information, the module version the Go toolchain records, a tag or a
// pseudo-version holding the source revision, +dirty for uncommitted changes.
// Without one, it is devel and the source revision when the build records it.
func Version() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if version := info.Main.Version; version != "" && version != "(devel)" {
		return version
	}
	version := "devel"
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			version += "+" + setting.Value[:min(12, len(setting.Value))]
		}
	}
	return version
}
