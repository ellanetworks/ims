package api

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"time"

	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
)

// MCPTTState is the live state of the running MCPTT AS, in its memory: its clients' registrations, their
// affiliations and the group calls, or one of them by its ID, and whether there is one. An error is of the AS not
// answering: restarting, or its calls busy past ctx, every one of them for Calls, which leaves out a call busy alone.
type MCPTTState interface {
	Registrations() ([]MCPTTRegistration, error)
	Affiliations() ([]MCPTTAffiliation, error)
	Calls(ctx context.Context) ([]MCPTTCall, error)
	Call(ctx context.Context, id string) (MCPTTCall, bool, error)
}

// MCPTTRegistration is the binding of an MCPTT client to its user by service authorisation (TS 24.379 §7.3.2): the
// IMPU it registered at, those it is served at, since when, until when; the contacts it registered, in direct mode.
type MCPTTRegistration struct {
	MCPTTID  string
	ClientID string
	IMPU     string
	IMPUs    []string
	Since    time.Time
	Expires  time.Time
	Contacts []string
}

// MCPTTAffiliation is the affiliation of an MCPTT client of a user to a group, as the participating function has it
// (TS 24.379 §9.2.2.2.2): affiliating, affiliated or deaffiliating. The first two count as affiliated, the last does
// not (§9.2.2.2.11 3a).
type MCPTTAffiliation struct {
	MCPTTID  string
	ClientID string
	GroupID  string
	Status   string
	// Implicit tells the server affiliated the client to the group from the user's <ImplicitAffiliations> (TS 24.379
	// §9.2.2.2.15), and the client's publications did not list it since.
	Implicit bool
}

// MCPTTCall is a group call (TS 24.379 §10.1.1.4): its session, its participants and its talker, if any. Started is
// zero while it is set up, Ends if no TNG3 releases it, Talker if the floor is idle.
type MCPTTCall struct {
	ID           string
	SessionID    string
	GroupID      string
	Originator   string
	Created      time.Time
	Started      time.Time
	Ends         time.Time
	Talker       string
	TalkingSince time.Time
	Participants []MCPTTParticipant
}

type MCPTTParticipant struct {
	MCPTTID     string
	Joined      bool
	Required    bool
	ReceiveOnly bool
}

// mcpttStateTimeout bounds a read of the calls: each waits for the strand of its group.
const mcpttStateTimeout = 2 * time.Second

type MCPTTRegistrationResponse struct {
	MCPTTID   string   `json:"mcptt_id"`
	ClientID  string   `json:"client_id"`
	IMPU      string   `json:"impu"`
	IMPUs     []string `json:"impus"`
	Since     string   `json:"since"`
	ExpiresAt string   `json:"expires_at"`
	Contacts  []string `json:"contacts"`
}

type MCPTTAffiliationResponse struct {
	MCPTTID  string `json:"mcptt_id"`
	ClientID string `json:"client_id"`
	GroupID  string `json:"group_id"`
	Status   string `json:"status"`
	// Implicit tells how the client was affiliated: by the server, from the user's <ImplicitAffiliations> (TS 24.379
	// §9.2.2.2.15), at its service authorisation or once the setting changed; false of the client's own affiliation
	// (§9.2.2.2.3), its publication listing the group, which an implicit one becomes.
	Implicit bool `json:"implicit"`
}

type MCPTTCallResponse struct {
	ID           string                     `json:"id"`
	SessionID    string                     `json:"session_id"`
	GroupID      string                     `json:"group_id"`
	Originator   string                     `json:"originator"`
	CreatedAt    string                     `json:"created_at"`
	StartedAt    *string                    `json:"started_at"`
	EndsAt       *string                    `json:"ends_at"`
	Talker       *MCPTTTalkerResponse       `json:"talker"`
	Participants []MCPTTParticipantResponse `json:"participants"`
}

type MCPTTTalkerResponse struct {
	MCPTTID string `json:"mcptt_id"`
	Since   string `json:"since"`
}

type MCPTTParticipantResponse struct {
	MCPTTID  string `json:"mcptt_id"`
	State    string `json:"state"`
	Required bool   `json:"required"`
	RecvOnly bool   `json:"recvonly"`
}

type mcpttPage[T any] struct {
	Items      []T `json:"items"`
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	TotalCount int `json:"total_count"`
}

// ListMCPTTRegistrations returns a page of the registered clients, by MCPTT ID and client ID, of the user of
// mcptt_id if given.
func ListMCPTTRegistrations(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, perPage, ok := pagination(w, r, cfg)
		if !ok {
			return
		}

		regs, err := cfg.MCPTTState.Registrations()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "MCPTT is unavailable", err, cfg.Logger)
			return
		}

		user := r.URL.Query().Get("mcptt_id")
		regs = slices.DeleteFunc(regs, func(g MCPTTRegistration) bool { return !matches(user, g.MCPTTID) })
		slices.SortFunc(regs, func(a, b MCPTTRegistration) int {
			return cmp.Or(cmp.Compare(a.MCPTTID, b.MCPTTID), cmp.Compare(a.ClientID, b.ClientID))
		})

		resp := mcpttPage[MCPTTRegistrationResponse]{Items: []MCPTTRegistrationResponse{}, Page: page, PerPage: perPage, TotalCount: len(regs)}
		for _, g := range paged(regs, page, perPage) {
			resp.Items = append(resp.Items, MCPTTRegistrationResponse{
				MCPTTID: g.MCPTTID, ClientID: g.ClientID, IMPU: g.IMPU, IMPUs: orEmpty(g.IMPUs), Since: formatTime(g.Since),
				ExpiresAt: formatTime(g.Expires), Contacts: orEmpty(g.Contacts),
			})
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

// ListMCPTTAffiliations returns a page of the affiliations, by group, MCPTT ID and client ID, of the user of
// mcptt_id and the group of group_id if given.
func ListMCPTTAffiliations(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, perPage, ok := pagination(w, r, cfg)
		if !ok {
			return
		}

		affs, err := cfg.MCPTTState.Affiliations()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "MCPTT is unavailable", err, cfg.Logger)
			return
		}

		q := r.URL.Query()
		user, group := q.Get("mcptt_id"), q.Get("group_id")
		affs = slices.DeleteFunc(affs, func(a MCPTTAffiliation) bool {
			return !matches(user, a.MCPTTID) || !matches(group, a.GroupID)
		})
		slices.SortFunc(affs, func(a, b MCPTTAffiliation) int {
			return cmp.Or(cmp.Compare(a.GroupID, b.GroupID), cmp.Compare(a.MCPTTID, b.MCPTTID), cmp.Compare(a.ClientID, b.ClientID))
		})

		resp := mcpttPage[MCPTTAffiliationResponse]{Items: []MCPTTAffiliationResponse{}, Page: page, PerPage: perPage, TotalCount: len(affs)}
		for _, a := range paged(affs, page, perPage) {
			resp.Items = append(resp.Items, MCPTTAffiliationResponse(a))
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

// ListMCPTTCalls returns a page of the group calls, oldest first, of the group of group_id and with the participant
// of mcptt_id if given.
func ListMCPTTCalls(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, perPage, ok := pagination(w, r, cfg)
		if !ok {
			return
		}

		calls, ok := mcpttCalls(w, r, cfg)
		if !ok {
			return
		}

		q := r.URL.Query()
		user, group := q.Get("mcptt_id"), q.Get("group_id")
		calls = slices.DeleteFunc(calls, func(c MCPTTCall) bool {
			return !matches(group, c.GroupID) ||
				user != "" && !slices.ContainsFunc(c.Participants, func(p MCPTTParticipant) bool { return matches(user, p.MCPTTID) })
		})

		resp := mcpttPage[MCPTTCallResponse]{Items: []MCPTTCallResponse{}, Page: page, PerPage: perPage, TotalCount: len(calls)}
		for _, c := range paged(calls, page, perPage) {
			resp.Items = append(resp.Items, mcpttCallResponse(c))
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

// GetMCPTTCall returns the group call of the path's call ID, the "gr" of its MCPTT session identity: of the strand of
// its group only.
func GetMCPTTCall(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), mcpttStateTimeout)
		defer cancel()

		call, found, err := cfg.MCPTTState.Call(ctx, r.PathValue("call_id"))

		switch {
		case err != nil:
			writeError(w, http.StatusServiceUnavailable, "MCPTT is unavailable", err, cfg.Logger)
		case !found:
			writeError(w, http.StatusNotFound, "call not found", nil, cfg.Logger)
		default:
			writeResponse(w, mcpttCallResponse(call), http.StatusOK, cfg.Logger)
		}
	})
}

// mcpttCalls are the calls, oldest first, or false once the error is answered.
func mcpttCalls(w http.ResponseWriter, r *http.Request, cfg Config) ([]MCPTTCall, bool) {
	ctx, cancel := context.WithTimeout(r.Context(), mcpttStateTimeout)
	defer cancel()

	calls, err := cfg.MCPTTState.Calls(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "MCPTT is unavailable", err, cfg.Logger)
		return nil, false
	}

	slices.SortFunc(calls, func(a, b MCPTTCall) int {
		return cmp.Or(a.Created.Compare(b.Created), cmp.Compare(a.ID, b.ID))
	})

	return calls, true
}

func mcpttCallResponse(c MCPTTCall) MCPTTCallResponse {
	resp := MCPTTCallResponse{
		ID: c.ID, SessionID: c.SessionID, GroupID: c.GroupID, Originator: c.Originator, CreatedAt: formatTime(c.Created),
		StartedAt: optionalTime(c.Started), EndsAt: optionalTime(c.Ends), Participants: []MCPTTParticipantResponse{},
	}

	if c.Talker != "" {
		resp.Talker = &MCPTTTalkerResponse{MCPTTID: c.Talker, Since: formatTime(c.TalkingSince)}
	}

	for _, p := range c.Participants {
		state := "invited"
		if p.Joined {
			state = "joined"
		}

		resp.Participants = append(resp.Participants, MCPTTParticipantResponse{
			MCPTTID: p.MCPTTID, State: state, Required: p.Required, RecvOnly: p.ReceiveOnly,
		})
	}

	return resp
}

func optionalTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}

	s := formatTime(t)

	return &s
}

// matches reports whether the identity id is that of filter, if any.
func matches(filter, id string) bool {
	return filter == "" || mcpttsettings.SameIdentity(filter, id)
}

// implicitlyAffiliated reports whether group is an implicit affiliation of the user of MCPTT ID id in s.
func implicitlyAffiliated(s mcpttsettings.Settings, id, group string) bool {
	i := s.UserIndex(id)

	return i >= 0 && slices.ContainsFunc(s.Users[i].ImplicitAffiliations, func(a string) bool {
		return mcpttsettings.SameIdentity(a, group)
	})
}
