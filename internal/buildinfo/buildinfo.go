// Package buildinfo gives the version of the agent binary.
package buildinfo

import "runtime/debug"

// Version is the version of the binary from its build information: the
// module version the Go toolchain records, a tag or a pseudo-version holding
// the source revision, +dirty for uncommitted changes. Without one, it is
// devel and the source revision when the build records it.
func Version() string {
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
