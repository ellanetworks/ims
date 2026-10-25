package api

import (
	"net/http"
	"net/url"
	"strings"

	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
)

// MCPTTGroup is an MCPTT group: the <list-service> of its group document (TS 24.481 §7.2) and the server's local policy
// for it. An absent optional field is of its default.
type MCPTTGroup struct {
	GroupID string `json:"group_id"`
	// InviteMembers is true, of a prearranged group, if absent: the only kind supported, chat groups to come.
	InviteMembers               *bool         `json:"invite_members,omitempty"`
	MaxParticipantCount         int           `json:"max_participant_count"`
	PreferredVoiceEncodings     []string      `json:"preferred_voice_encodings"`
	HangTimerMS                 *int64        `json:"hang_timer_ms,omitempty"`
	MaximumDurationMS           int64         `json:"maximum_duration_ms"`
	MinimumNumberToStart        int           `json:"minimum_number_to_start"`
	TimeoutForAcknowledgementMS int64         `json:"timeout_for_acknowledgement_of_required_members_ms"`
	ActionUponTimeout           string        `json:"action_upon_expiration_of_timeout_for_acknowledgement_of_required_members"`
	MinimumNumberOfAffiliated   int           `json:"minimum_number_of_affiliated_members"`
	ReleaseOnInitiatorLeave     bool          `json:"release_on_initiator_leave"`
	Members                     []MCPTTMember `json:"members"`
}

// MCPTTMember is an <entry> of the group's list: an MCPTT user.
type MCPTTMember struct {
	MCPTTID             string `json:"mcptt_id"`
	UserPriority        int    `json:"user_priority"`
	Required            bool   `json:"required"`
	RecvOnly            bool   `json:"recvonly"`
	AffiliationRequired bool   `json:"affiliation_required"`
}

type MCPTTGroups struct {
	Items      []MCPTTGroup `json:"items"`
	Page       int          `json:"page"`
	PerPage    int          `json:"per_page"`
	TotalCount int          `json:"total_count"`
}

// ListMCPTTGroups returns a page of the groups, in the order they were created in.
func ListMCPTTGroups(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, perPage, ok := pagination(w, r, cfg)
		if !ok {
			return
		}

		groups := cfg.MCPTT.Get().Groups
		resp := MCPTTGroups{Items: []MCPTTGroup{}, Page: page, PerPage: perPage, TotalCount: len(groups)}

		for _, g := range paged(groups, page, perPage) {
			resp.Items = append(resp.Items, mcpttGroupResponse(g))
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

func GetMCPTTGroup(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := cfg.MCPTT.Get()

		i := s.GroupIndex(r.PathValue("group_id"))
		if i < 0 {
			writeError(w, http.StatusNotFound, "group not found", nil, cfg.Logger)
			return
		}

		writeResponse(w, mcpttGroupResponse(s.Groups[i]), http.StatusOK, cfg.Logger)
	})
}

func CreateMCPTTGroup(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p MCPTTGroup
		if !decodeStrictly(w, r, &p, cfg.Logger) {
			return
		}

		g, problem := mcpttGroupSettings(p)
		if problem != "" {
			invalidMCPTT(w, problem, cfg.Logger)
			return
		}

		g, err := cfg.MCPTT.CreateGroup(r.Context(), g)
		if err != nil {
			writeMCPTTSettingsError(w, err, "Failed to create the group", mcpttField, cfg.Logger)
			return
		}

		w.Header().Set("Location", "/api/v1/mcptt/groups/"+url.PathEscape(g.URI))
		writeResponse(w, mcpttGroupResponse(g), http.StatusCreated, cfg.Logger)
	})
}

// UpdateMCPTTGroup replaces the group of the path, members included. The group ID of the body, if any, is the group's.
func UpdateMCPTTGroup(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("group_id")

		var p MCPTTGroup
		if !decodeStrictly(w, r, &p, cfg.Logger) {
			return
		}

		if p.GroupID != "" && !mcpttsettings.SameIdentity(p.GroupID, id) {
			invalidMCPTT(w, "group_id: "+p.GroupID+" is not the group ID of the path, which a group keeps", cfg.Logger)
			return
		}

		p.GroupID = id

		g, problem := mcpttGroupSettings(p)
		if problem != "" {
			invalidMCPTT(w, problem, cfg.Logger)
			return
		}

		g, err := cfg.MCPTT.UpdateGroup(r.Context(), g)
		if err != nil {
			writeMCPTTSettingsError(w, err, "Failed to update the group", mcpttField, cfg.Logger)
			return
		}

		writeResponse(w, mcpttGroupResponse(g), http.StatusOK, cfg.Logger)
	})
}

// DeleteMCPTTGroup deletes the group of the path, unless it is an implicit affiliation of a user.
func DeleteMCPTTGroup(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := cfg.MCPTT.DeleteGroup(r.Context(), r.PathValue("group_id")); err != nil {
			writeMCPTTSettingsError(w, err, "Failed to delete the group", mcpttField, cfg.Logger)
			return
		}

		writeResponse(w, Message{Message: "group deleted"}, http.StatusOK, cfg.Logger)
	})
}

// PutMCPTTMember adds the user of the path to the members of the group, after the others, with a 201, or replaces the
// member where it is; the group as it is then. The MCPTT ID of the body, if any, is the path's.
func PutMCPTTMember(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		group, id := r.PathValue("group_id"), r.PathValue("mcptt_id")

		var p MCPTTMember
		if !decodeStrictly(w, r, &p, cfg.Logger) {
			return
		}

		if p.MCPTTID != "" && !mcpttsettings.SameIdentity(p.MCPTTID, id) {
			invalidMCPTT(w, "mcptt_id: "+p.MCPTTID+" is not the MCPTT ID of the path", cfg.Logger)
			return
		}

		p.MCPTTID = id

		g, added, err := cfg.MCPTT.PutMember(r.Context(), group, mcpttEntrySettings(p))
		if err != nil {
			writeMCPTTSettingsError(w, err, "Failed to put the member", mcpttMemberField, cfg.Logger)
			return
		}

		status := http.StatusOK
		if added {
			status = http.StatusCreated
		}

		writeResponse(w, mcpttGroupResponse(g), status, cfg.Logger)
	})
}

// DeleteMCPTTMember removes the user of the path from the members of the group; the group as it is then.
func DeleteMCPTTMember(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		group := r.PathValue("group_id")

		g, err := cfg.MCPTT.DeleteMember(r.Context(), group, r.PathValue("mcptt_id"))
		if err != nil {
			writeMCPTTSettingsError(w, err, "Failed to delete the member", mcpttField, cfg.Logger)
			return
		}

		writeResponse(w, mcpttGroupResponse(g), http.StatusOK, cfg.Logger)
	})
}

// mcpttGroupSettings is the group of p, or the problem with its durations.
func mcpttGroupSettings(p MCPTTGroup) (mcpttsettings.Group, string) {
	g := mcpttsettings.Group{
		URI: p.GroupID, InviteMembers: p.InviteMembers == nil || *p.InviteMembers, MaxParticipants: p.MaxParticipantCount,
		Encodings: p.PreferredVoiceEncodings, MinimumToStart: p.MinimumNumberToStart,
		TNG1Action: mcpttsettings.Action(p.ActionUponTimeout), MinimumAffiliated: p.MinimumNumberOfAffiliated,
		ReleaseOnInitiatorLeave: p.ReleaseOnInitiatorLeave,
	}

	var problems []string

	add := func(problem string) {
		if problem != "" {
			problems = append(problems, problem)
		}
	}

	var problem string

	g.T4, problem = optionalDuration("hang_timer_ms", p.HangTimerMS)
	add(problem)
	g.TNG3, problem = duration("maximum_duration_ms", p.MaximumDurationMS)
	add(problem)
	g.TNG1, problem = duration("timeout_for_acknowledgement_of_required_members_ms", p.TimeoutForAcknowledgementMS)
	add(problem)

	for _, m := range p.Members {
		g.List.Entries = append(g.List.Entries, mcpttEntrySettings(m))
	}

	return g, strings.Join(problems, "; ")
}

func mcpttEntrySettings(m MCPTTMember) mcpttsettings.Entry {
	return mcpttsettings.Entry{
		URI: m.MCPTTID, UserPriority: m.UserPriority, Required: m.Required, ReceiveOnly: m.RecvOnly,
		AffiliationRequired: m.AffiliationRequired,
	}
}

func mcpttGroupResponse(g mcpttsettings.Group) MCPTTGroup {
	resp := MCPTTGroup{
		GroupID: g.URI, InviteMembers: &g.InviteMembers, MaxParticipantCount: g.MaxParticipants,
		PreferredVoiceEncodings: orEmpty(g.Encodings), HangTimerMS: optionalMilliseconds(g.T4),
		MaximumDurationMS: milliseconds(g.TNG3), MinimumNumberToStart: g.MinimumToStart,
		TimeoutForAcknowledgementMS: milliseconds(g.TNG1), ActionUponTimeout: string(g.TNG1Action),
		MinimumNumberOfAffiliated: g.MinimumAffiliated, ReleaseOnInitiatorLeave: g.ReleaseOnInitiatorLeave,
		Members: []MCPTTMember{},
	}

	for _, e := range g.List.Entries {
		resp.Members = append(resp.Members, MCPTTMember{
			MCPTTID: e.URI, UserPriority: e.UserPriority, Required: e.Required, RecvOnly: e.ReceiveOnly,
			AffiliationRequired: e.AffiliationRequired,
		})
	}

	return resp
}
