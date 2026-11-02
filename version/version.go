package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var version string

var GitCommit string

type Info struct {
	Version  string
	Revision string
}

func Get() Info {
	return Info{Version: strings.TrimSpace(version), Revision: GitCommit}
}
