package cluster

import (
	"testing"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/hashicorp/raft"
)

func TestNextSchema(t *testing.T) {
	servers := []raft.Server{{ID: "a"}, {ID: "b"}, {ID: "c", Suffrage: raft.Nonvoter}}
	members := func(b, c int) []db.Member {
		return []db.Member{{NodeID: "a", SchemaVersion: 3}, {NodeID: "b", SchemaVersion: b}, {NodeID: "c", SchemaVersion: c}}
	}

	tests := []struct {
		name     string
		current  int
		own      int
		members  []db.Member
		want     int
		upgraded bool
	}{
		{name: "every member supports the next schema", current: 1, own: 3, members: members(2, 3), want: 2, upgraded: true},
		{name: "one step at a time", current: 2, own: 3, members: members(3, 3), want: 3, upgraded: true},
		{name: "a member lags, even a non-voter", current: 1, own: 3, members: members(3, 1)},
		{name: "a member without a row", current: 1, own: 3, members: members(3, 3)[:2]},
		{name: "this binary is the limit", current: 3, own: 3, members: members(4, 4)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := nextSchema(tt.current, tt.own, servers, tt.members)
			if got != tt.want || ok != tt.upgraded {
				t.Fatalf("nextSchema = %d, %t; want %d, %t", got, ok, tt.want, tt.upgraded)
			}
		})
	}
}
