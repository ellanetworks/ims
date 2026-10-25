package api

import (
	"net/http"
	"net/url"
	"strings"

	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
)

// MCPTTGroup is an MCPTT group: of the <list-service> of its group document (TS 24.481 §7.2) and the server's local
// policy for it, what an operator sets. The group is a prearranged group, the only kind supported, of AMR-WB (TS
// 26.179 §4.1.1); the server fixes the rest of the document to its defaults: no limit of participants, no minimum
// to start nor of affiliated members, no required member.
type MCPTTGroup struct {
	GroupID                 string        `json:"group_id"`
	DisplayName             string        `json:"display_name"`
	HangTimerMS             *int64        `json:"hang_timer_ms,omitempty"`
	MaximumDurationMS       int64         `json:"maximum_duration_ms"`
	ReleaseOnInitiatorLeave bool          `json:"release_on_initiator_leave"`
	Members                 []MCPTTMember `json:"members"`
}

// MCPTTMember is an <entry> of the group's list: an MCPTT user, receive-only or not, of the lowest priority, neither
// required nor required to be affiliated. ImplicitAffiliation is of the user's profile, not of the entry: whether the
// group is one of the user's implicit affiliations (TS 24.484 §8.3.2.7). Of a request, absent keeps what the user
// has; removing a member removes it.
type MCPTTMember struct {
	MCPTTID             string `json:"mcptt_id"`
	RecvOnly            bool   `json:"recvonly"`
	ImplicitAffiliation *bool  `json:"implicit_affiliation,omitempty"`
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

		s := cfg.MCPTT.Get()
		resp := MCPTTGroups{Items: []MCPTTGroup{}, Page: page, PerPage: perPage, TotalCount: len(s.Groups)}

		for _, g := range paged(s.Groups, page, perPage) {
			resp.Items = append(resp.Items, mcpttGroupResponse(g, s))
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

		writeResponse(w, mcpttGroupResponse(s.Groups[i], s), http.StatusOK, cfg.Logger)
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

		g, err := cfg.MCPTT.CreateGroup(r.Context(), g, mcpttImplicit(p.Members))
		if err != nil {
			writeMCPTTSettingsError(w, err, "Failed to create the group", mcpttField, cfg.Logger)
			return
		}

		w.Header().Set("Location", "/api/v1/mcptt/groups/"+url.PathEscape(g.URI))
		writeResponse(w, mcpttGroupResponse(g, cfg.MCPTT.Get()), http.StatusCreated, cfg.Logger)
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

		g, err := cfg.MCPTT.UpdateGroup(r.Context(), g, mcpttImplicit(p.Members))
		if err != nil {
			writeMCPTTSettingsError(w, err, "Failed to update the group", mcpttField, cfg.Logger)
			return
		}

		writeResponse(w, mcpttGroupResponse(g, cfg.MCPTT.Get()), http.StatusOK, cfg.Logger)
	})
}

// DeleteMCPTTGroup deletes the group of the path, which goes from the implicit affiliations of its users.
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

		g, added, err := cfg.MCPTT.PutMember(r.Context(), group, mcpttEntrySettings(p), p.ImplicitAffiliation)
		if err != nil {
			writeMCPTTSettingsError(w, err, "Failed to put the member", mcpttMemberField, cfg.Logger)
			return
		}

		status := http.StatusOK
		if added {
			status = http.StatusCreated
		}

		writeResponse(w, mcpttGroupResponse(g, cfg.MCPTT.Get()), status, cfg.Logger)
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

		writeResponse(w, mcpttGroupResponse(g, cfg.MCPTT.Get()), http.StatusOK, cfg.Logger)
	})
}

// mcpttImplicit is the implicit affiliation of the members that have one.
func mcpttImplicit(members []MCPTTMember) mcpttsettings.Implicit {
	implicit := mcpttsettings.Implicit{}

	for _, m := range members {
		if m.ImplicitAffiliation != nil {
			implicit[m.MCPTTID] = *m.ImplicitAffiliation
		}
	}

	return implicit
}

// mcpttGroupSettings is the group of p, or the problem with its durations.
func mcpttGroupSettings(p MCPTTGroup) (mcpttsettings.Group, string) {
	g := mcpttsettings.Group{
		URI: p.GroupID, DisplayName: p.DisplayName, InviteMembers: true, ReleaseOnInitiatorLeave: p.ReleaseOnInitiatorLeave,
	}

	var problems []string

	t4, problem := optionalDuration("hang_timer_ms", p.HangTimerMS)
	if problem != "" {
		problems = append(problems, problem)
	}

	tng3, problem := duration("maximum_duration_ms", p.MaximumDurationMS)
	if problem != "" {
		problems = append(problems, problem)
	}

	g.T4, g.TNG3 = t4, tng3

	for _, m := range p.Members {
		g.List.Entries = append(g.List.Entries, mcpttEntrySettings(m))
	}

	return g, strings.Join(problems, "; ")
}

func mcpttEntrySettings(m MCPTTMember) mcpttsettings.Entry {
	return mcpttsettings.Entry{URI: m.MCPTTID, ReceiveOnly: m.RecvOnly}
}

// mcpttGroupResponse is g, its members' implicit affiliations those of their users of s.
func mcpttGroupResponse(g mcpttsettings.Group, s mcpttsettings.Settings) MCPTTGroup {
	resp := MCPTTGroup{
		GroupID: g.URI, DisplayName: g.DisplayName, HangTimerMS: optionalMilliseconds(g.T4),
		MaximumDurationMS: milliseconds(g.TNG3), ReleaseOnInitiatorLeave: g.ReleaseOnInitiatorLeave, Members: []MCPTTMember{},
	}

	for _, e := range g.List.Entries {
		implicit := implicitlyAffiliated(s, e.URI, g.URI)

		resp.Members = append(resp.Members, MCPTTMember{MCPTTID: e.URI, RecvOnly: e.ReceiveOnly, ImplicitAffiliation: &implicit})
	}

	return resp
}
