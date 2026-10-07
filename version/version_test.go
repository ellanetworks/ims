package version

import (
	"regexp"
	"testing"
)

func TestGet(t *testing.T) {
	if v := Get().Version; !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(v) {
		t.Fatalf("Version = %q, want vMAJOR.MINOR.PATCH", v)
	}
}
