package transport

import "testing"

func TestDefaultMaxConnections(t *testing.T) {
	for _, c := range []struct{ openFiles, want int }{
		{524288, 393216},
		{1024, 768},
		{1, DefaultMaxConnections},
		{0, DefaultMaxConnections},
	} {
		if got := defaultMaxConnections(c.openFiles); got != c.want {
			t.Errorf("defaultMaxConnections(%d) = %d, want %d", c.openFiles, got, c.want)
		}
	}

	if openFileLimit() <= 0 {
		t.Errorf("openFileLimit() = %d", openFileLimit())
	}
}
