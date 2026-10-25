package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"
)

// fakeMCPTTState is the live state of an AS, or err.
type fakeMCPTTState struct {
	regs  []MCPTTRegistration
	affs  []MCPTTAffiliation
	calls []MCPTTCall
	err   error
}

func (f fakeMCPTTState) Registrations() ([]MCPTTRegistration, error) { return f.regs, f.err }

func (f fakeMCPTTState) Affiliations() ([]MCPTTAffiliation, error) { return f.affs, f.err }

func (f fakeMCPTTState) Calls(context.Context) ([]MCPTTCall, error) { return f.calls, f.err }

func (f fakeMCPTTState) Call(_ context.Context, id string) (MCPTTCall, bool, error) {
	i := slices.IndexFunc(f.calls, func(c MCPTTCall) bool { return c.ID == id })
	if f.err != nil || i < 0 {
		return MCPTTCall{}, false, f.err
	}

	return f.calls[i], true, nil
}

// withState is e serving state, of e's settings.
func (e *mcpttEnv) withState(state MCPTTState) *mcpttEnv {
	e.h = NewHandler(Config{MCPTT: e.live, MCPTTState: state, Logger: slog.New(slog.DiscardHandler)})
	return e
}

var t0 = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func liveState() fakeMCPTTState {
	return fakeMCPTTState{
		regs: []MCPTTRegistration{
			{MCPTTID: "sip:bob@mcptt.example.com", ClientID: "b1", IMPU: "sip:bob@ims.example.com", Since: t0, Expires: t0.Add(time.Hour)},
			{MCPTTID: "sip:alice@mcptt.example.com", ClientID: "a2", IMPU: "sip:alice@ims.example.com", Since: t0, Expires: t0.Add(time.Hour)},
			{MCPTTID: "sip:alice@mcptt.example.com", ClientID: "a1", IMPU: "sip:alice@ims.example.com", Since: t0, Expires: t0.Add(time.Hour)},
		},
		affs: []MCPTTAffiliation{
			{MCPTTID: "sip:bob@mcptt.example.com", ClientID: "b1", GroupID: "sip:fire@mcptt.example.com", Status: "affiliating"},
			{MCPTTID: "sip:alice@mcptt.example.com", ClientID: "a1", GroupID: "sip:fire@mcptt.example.com", Status: "affiliated"},
		},
		calls: []MCPTTCall{
			{
				ID: "late", GroupID: "sip:fire@mcptt.example.com", Originator: "sip:bob@mcptt.example.com", Created: t0.Add(time.Minute),
				Participants: []MCPTTParticipant{{MCPTTID: "sip:bob@mcptt.example.com", Joined: true}},
			},
			{
				ID: "early", SessionID: "sip:mcptt-controlling@mcptt.example.com;gr=early", GroupID: "sip:police@mcptt.example.com",
				Originator: "sip:alice@mcptt.example.com", Created: t0, Started: t0.Add(time.Second), Ends: t0.Add(10 * time.Minute),
				Talker: "sip:alice@mcptt.example.com", TalkingSince: t0.Add(2 * time.Second),
				Participants: []MCPTTParticipant{
					{MCPTTID: "sip:alice@mcptt.example.com", Joined: true},
					{MCPTTID: "sip:carol@mcptt.example.com", Required: true, ReceiveOnly: true},
				},
			},
		},
	}
}

// The registrations by MCPTT ID and client ID, filtered by user, paged.
func TestMCPTTRegistrations(t *testing.T) {
	e := newMCPTTEnv(t).withState(liveState())

	var page mcpttPage[MCPTTRegistrationResponse]

	e.ok(http.StatusOK, "GET", "/api/v1/mcptt/registrations?mcptt_id=sip:alice@MCPTT.example.com&per_page=1&page=2", "", &page)

	want := MCPTTRegistrationResponse{
		MCPTTID: "sip:alice@mcptt.example.com", ClientID: "a2", IMPU: "sip:alice@ims.example.com", IMPUs: []string{},
		Since: "2026-10-25T12:00:00.000Z", ExpiresAt: "2026-10-25T13:00:00.000Z", Contacts: []string{},
	}
	if page.TotalCount != 2 || len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0], want) {
		t.Errorf("page %+v", page)
	}
}

// The affiliations by group, of whether the group is configured as an implicit affiliation of the user's.
func TestMCPTTAffiliations(t *testing.T) {
	e := newMCPTTEnv(t).withState(liveState())
	e.provision()

	var page mcpttPage[MCPTTAffiliationResponse]

	e.ok(http.StatusOK, "GET", "/api/v1/mcptt/affiliations?group_id=sip:fire@mcptt.example.com", "", &page)

	want := []MCPTTAffiliationResponse{
		{MCPTTID: "sip:alice@mcptt.example.com", ClientID: "a1", GroupID: "sip:fire@mcptt.example.com", Status: "affiliated", ConfiguredImplicit: true},
		{MCPTTID: "sip:bob@mcptt.example.com", ClientID: "b1", GroupID: "sip:fire@mcptt.example.com", Status: "affiliating"},
	}
	if !reflect.DeepEqual(page.Items, want) {
		t.Errorf("affiliations %+v", page.Items)
	}

	e.ok(http.StatusOK, "GET", "/api/v1/mcptt/affiliations?group_id=sip:police@mcptt.example.com", "", &page)

	if page.TotalCount != 0 || page.Items == nil {
		t.Errorf("affiliations to police %+v", page)
	}
}

// The calls, oldest first, filtered by group and participant; one by its ID.
func TestMCPTTCalls(t *testing.T) {
	e := newMCPTTEnv(t).withState(liveState())

	var page mcpttPage[MCPTTCallResponse]

	e.ok(http.StatusOK, "GET", "/api/v1/mcptt/calls", "", &page)

	if len(page.Items) != 2 || page.Items[0].ID != "early" || page.Items[1].StartedAt != nil || page.Items[1].Talker != nil {
		t.Errorf("calls %+v", page.Items)
	}

	e.ok(http.StatusOK, "GET", "/api/v1/mcptt/calls?mcptt_id=sip:carol@mcptt.example.com", "", &page)

	if len(page.Items) != 1 || page.Items[0].ID != "early" {
		t.Errorf("calls of carol %+v", page.Items)
	}

	var c MCPTTCallResponse

	e.ok(http.StatusOK, "GET", "/api/v1/mcptt/calls/early", "", &c)

	started, ends := "2026-10-25T12:00:01.000Z", "2026-10-25T12:10:00.000Z"

	want := MCPTTCallResponse{
		ID: "early", SessionID: "sip:mcptt-controlling@mcptt.example.com;gr=early", GroupID: "sip:police@mcptt.example.com",
		Originator: "sip:alice@mcptt.example.com", CreatedAt: "2026-10-25T12:00:00.000Z", StartedAt: &started, EndsAt: &ends,
		Talker: &MCPTTTalkerResponse{MCPTTID: "sip:alice@mcptt.example.com", Since: "2026-10-25T12:00:02.000Z"},
		Participants: []MCPTTParticipantResponse{
			{MCPTTID: "sip:alice@mcptt.example.com", State: "joined"},
			{MCPTTID: "sip:carol@mcptt.example.com", State: "invited", Required: true, RecvOnly: true},
		},
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("call\n%+v\nwant\n%+v", c, want)
	}

	e.fails(http.StatusNotFound, "GET", "/api/v1/mcptt/calls/gone", "", "call not found")
}

// The state of an AS that does not answer is unavailable.
func TestMCPTTStateUnavailable(t *testing.T) {
	e := newMCPTTEnv(t).withState(fakeMCPTTState{err: errors.New("MCPTT is restarting")})

	for _, path := range []string{"/api/v1/mcptt/registrations", "/api/v1/mcptt/affiliations", "/api/v1/mcptt/calls", "/api/v1/mcptt/calls/x"} {
		e.fails(http.StatusServiceUnavailable, "GET", path, "", "MCPTT is unavailable")
	}
}
