package main

import (
	"fmt"
	"runtime"
)

var (
	// Version is the current version of the application
	Version = "dev"
	// BuildTime is the time the binary was built
	BuildTime = "unknown"
	// GitCommit is the git commit hash
	GitCommit = "unknown"
)

// Info contains version information
type Info struct {
	Version   string
	BuildTime string
	GitCommit string
	GoVersion string
}

// Get returns the version information
func Get() Info {
	return Info{
		Version:   Version,
		BuildTime: BuildTime,
		GitCommit: GitCommit,
		GoVersion: runtime.Version(),
	}
}

// String returns the version information as a string
func String() string {
	return fmt.Sprintf("Version: %s\nBuild Time: %s\nGit Commit: %s\nGo Version: %s",
		Version, BuildTime, GitCommit, runtime.Version())
}
