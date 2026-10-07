package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var version string

// GitCommit is the git commit hash, set at build time with
// -ldflags "-X github.com/ellanetworks/ims/version.GitCommit=<hash>".
var GitCommit string

type Info struct {
	Version  string
	Revision string
}

func Get() Info {
	return Info{Version: strings.TrimSpace(version), Revision: GitCommit}
}
