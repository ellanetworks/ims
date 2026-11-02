package api

import (
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func Routes() map[string]bool {
	cfg := Config{Frontend: fstest.MapFS{}, BcryptCost: bcrypt.MinCost, Now: time.Now}
	out := map[string]bool{}

	for _, r := range routes(cfg, newAuth(cfg)) {
		out[r.pattern] = r.perm == public
	}

	return out
}
