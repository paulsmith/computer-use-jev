// Package version reports the herbie build identity.
package version

import "runtime/debug"

// release is set with -ldflags "-X github.com/paulsmith/computeruser/internal/herbie/version.release=vX.Y.Z" for tagged builds.
var release string

func String() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return identity(release, "", false)
	}
	var revision string
	var dirty bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	return identity(release, revision, dirty)
}

func identity(release, revision string, dirty bool) string {
	version := release
	if version == "" {
		version = revision
	}
	if version == "" {
		version = "devel"
	}
	if dirty {
		version += "+"
	}
	return version
}
