package db

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func testCall(caller, callee string, endedAt time.Time) Call {
	return Call{
		CallID:    "call-" + caller + "-" + callee,
		Caller:    caller,
		Callee:    callee,
		StartedAt: endedAt.Add(-time.Minute),
		EndedAt:   endedAt,
		Outcome:   OutcomeBusy,
		SIPStatus: 486,
		EndedBy:   PartyCallee,
	}
}

func mustCreateCall(t *testing.T, d *DB, c Call) int64 {
	t.Helper()

	id, err := d.CreateCall(context.Background(), c)
	if err != nil {
		t.Fatalf("CreateCall: %v", err)
	}

	return id
}

func TestCallRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	answered := testCall("tel:+15551230001", "tel:+15551230002", testNow)
	answered.AnsweredAt = testNow.Add(-30 * time.Second)
	answered.Outcome = OutcomeAnswered
	answered.SIPStatus = 200
	answered.EndedBy = PartyNetwork
	answered.Reason = "RELEASE_CAUSE;cause=2"
	answered.ID = mustCreateCall(t, d, answered)

	busy := testCall("tel:+15551230001", "tel:+15551230003", testNow)
	busy.ID = mustCreateCall(t, d, busy)

	calls, total, err := d.ListCalls(ctx, CallFilter{}, 1, 10)
	if err != nil {
		t.Fatalf("ListCalls: %v", err)
	}

	if total != 2 || !reflect.DeepEqual(calls, []Call{busy, answered}) {
		t.Fatalf("ListCalls =\n%+v\nwant\n%+v", calls, []Call{busy, answered})
	}
}

func TestCreateCallRejectsUnknownOutcome(t *testing.T) {
	c := testCall("tel:+15551230001", "tel:+15551230002", testNow)
	c.Outcome = "dropped"

	if _, err := openTestDB(t).CreateCall(context.Background(), c); err == nil {
		t.Fatal("CreateCall succeeded with an unknown outcome")
	}
}

func TestListCallsFilterAndPagination(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	const alice, bob, carol = "tel:+15551230001", "tel:+15551230002", "tel:+15551230003"

	ids := []int64{
		mustCreateCall(t, d, testCall(alice, bob, testNow)),
		mustCreateCall(t, d, testCall(bob, carol, testNow)),
		mustCreateCall(t, d, testCall(carol, alice, testNow)),
		mustCreateCall(t, d, testCall(bob, alice, testNow)),
		mustCreateCall(t, d, testCall(alice, carol, testNow)),
	}

	tests := []struct {
		name    string
		filter  CallFilter
		page    int
		perPage int
		total   int
		want    []int64
	}{
		{"all first page", CallFilter{}, 1, 2, 5, []int64{ids[4], ids[3]}},
		{"all last page", CallFilter{}, 3, 2, 5, []int64{ids[0]}},
		{"caller or callee", CallFilter{Number: alice}, 1, 10, 4, []int64{ids[4], ids[3], ids[2], ids[0]}},
		{"filtered second page", CallFilter{Number: alice}, 2, 3, 4, []int64{ids[0]}},
		{"no match", CallFilter{Number: "tel:+15559999999"}, 1, 10, 0, []int64{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, total, err := d.ListCalls(ctx, tt.filter, tt.page, tt.perPage)
			if err != nil {
				t.Fatalf("ListCalls: %v", err)
			}

			got := []int64{}
			for _, c := range calls {
				got = append(got, c.ID)
			}

			if total != tt.total || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ListCalls = %v (total %d), want %v (total %d)", got, total, tt.want, tt.total)
			}
		})
	}
}

func TestDeleteCallsEndedBefore(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	mustCreateCall(t, d, testCall("a", "b", testNow.Add(-time.Nanosecond)))
	atCutoff := mustCreateCall(t, d, testCall("a", "c", testNow))
	after := mustCreateCall(t, d, testCall("a", "d", testNow.Add(time.Nanosecond)))

	n, err := d.DeleteCallsEndedBefore(ctx, testNow)
	if err != nil {
		t.Fatalf("DeleteCallsEndedBefore: %v", err)
	}

	if n != 1 {
		t.Fatalf("deleted %d calls, want 1", n)
	}

	calls, _, err := d.ListCalls(ctx, CallFilter{}, 1, 10)
	if err != nil {
		t.Fatalf("ListCalls: %v", err)
	}

	if len(calls) != 2 || calls[0].ID != after || calls[1].ID != atCutoff {
		t.Fatalf("remaining = %+v", calls)
	}
}
