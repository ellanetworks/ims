package settings

import (
	"fmt"
	"testing"
	"time"
)

// BenchmarkNew validates settings of the most users the server serves, each a member of one of 10 groups of 100:
// what every change of Live validates whole.
func BenchmarkNew(b *testing.B) {
	s := Settings{
		System: System{ParticipatingPSI: "sip:pf@example.com", ControllingPSI: "sip:cf@example.com"},
		ServiceConfiguration: ServiceConfiguration{
			Domain: "example.com", OnNetwork: OnNetwork{FCTimersCounters: FCTimersCounters{T7: ptr(time.Second)}},
		},
	}

	for g := range MaxUsers / 100 {
		grp := Group{URI: fmt.Sprintf("sip:g%d@example.com", g), InviteMembers: true, TNG3: time.Minute}

		for m := range 100 {
			grp.List.Entries = append(grp.List.Entries, Entry{URI: fmt.Sprintf("sip:u%d@example.com", g*100+m)})
		}

		s.Groups = append(s.Groups, grp)
	}

	for u := range MaxUsers {
		s.Users = append(s.Users, User{
			MCPTTUserID: fmt.Sprintf("sip:u%d@example.com", u), IMPUs: []string{fmt.Sprintf("sip:u%d@ims.example.com", u)},
			ImplicitAffiliations: []string{fmt.Sprintf("sip:g%d@example.com", u/100)},
		})
	}

	for b.Loop() {
		if _, err := New(s); err != nil {
			b.Fatal(err)
		}
	}
}
