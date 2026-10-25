package api

import (
	"net/http"
	"net/url"

	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
)

// MCPTTUser is an MCPTT user: of its user profile (TS 24.484 §8.3), what an operator sets, and its IMS public user
// identities. The server fixes the rest: no limit of N2 nor N6.
type MCPTTUser struct {
	MCPTTID              string   `json:"mcptt_id"`
	Alias                string   `json:"alias"`
	IMPUs                []string `json:"impus"`
	ImplicitAffiliations []string `json:"implicit_affiliations"`
}

type MCPTTUsers struct {
	Items      []MCPTTUser `json:"items"`
	Page       int         `json:"page"`
	PerPage    int         `json:"per_page"`
	TotalCount int         `json:"total_count"`
}

// ListMCPTTUsers returns a page of the users, in the order they were created in.
func ListMCPTTUsers(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, perPage, ok := pagination(w, r, cfg)
		if !ok {
			return
		}

		users := cfg.MCPTT.Get().Users
		resp := MCPTTUsers{Items: []MCPTTUser{}, Page: page, PerPage: perPage, TotalCount: len(users)}

		for _, u := range paged(users, page, perPage) {
			resp.Items = append(resp.Items, mcpttUserResponse(u))
		}

		writeResponse(w, resp, http.StatusOK, cfg.Logger)
	})
}

func GetMCPTTUser(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := cfg.MCPTT.Get()

		i := s.UserIndex(r.PathValue("mcptt_id"))
		if i < 0 {
			writeError(w, http.StatusNotFound, "user not found", nil, cfg.Logger)
			return
		}

		writeResponse(w, mcpttUserResponse(s.Users[i]), http.StatusOK, cfg.Logger)
	})
}

func CreateMCPTTUser(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p MCPTTUser
		if !decodeStrictly(w, r, &p, cfg.Logger) {
			return
		}

		u, err := cfg.MCPTT.CreateUser(r.Context(), mcpttUserSettings(p))
		if err != nil {
			writeMCPTTSettingsError(w, err, "Failed to create the user", mcpttField, cfg.Logger)
			return
		}

		w.Header().Set("Location", "/api/v1/mcptt/users/"+url.PathEscape(u.MCPTTUserID))
		writeResponse(w, mcpttUserResponse(u), http.StatusCreated, cfg.Logger)
	})
}

// UpdateMCPTTUser replaces the user of the path. The MCPTT ID of the body, if any, is the user's.
func UpdateMCPTTUser(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("mcptt_id")

		var p MCPTTUser
		if !decodeStrictly(w, r, &p, cfg.Logger) {
			return
		}

		if p.MCPTTID != "" && !mcpttsettings.SameIdentity(p.MCPTTID, id) {
			invalidMCPTT(w, "mcptt_id: "+p.MCPTTID+" is not the MCPTT ID of the path, which a user keeps", cfg.Logger)
			return
		}

		p.MCPTTID = id

		u, err := cfg.MCPTT.UpdateUser(r.Context(), mcpttUserSettings(p))
		if err != nil {
			writeMCPTTSettingsError(w, err, "Failed to update the user", mcpttField, cfg.Logger)
			return
		}

		writeResponse(w, mcpttUserResponse(u), http.StatusOK, cfg.Logger)
	})
}

// DeleteMCPTTUser deletes the user of the path, which leaves its groups.
func DeleteMCPTTUser(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := cfg.MCPTT.DeleteUser(r.Context(), r.PathValue("mcptt_id")); err != nil {
			writeMCPTTSettingsError(w, err, "Failed to delete the user", mcpttField, cfg.Logger)
			return
		}

		writeResponse(w, Message{Message: "user deleted"}, http.StatusOK, cfg.Logger)
	})
}

func mcpttUserSettings(p MCPTTUser) mcpttsettings.User {
	return mcpttsettings.User{
		MCPTTUserID: p.MCPTTID, Alias: p.Alias, IMPUs: p.IMPUs, ImplicitAffiliations: p.ImplicitAffiliations,
	}
}

func mcpttUserResponse(u mcpttsettings.User) MCPTTUser {
	return MCPTTUser{
		MCPTTID: u.MCPTTUserID, Alias: u.Alias, IMPUs: orEmpty(u.IMPUs), ImplicitAffiliations: orEmpty(u.ImplicitAffiliations),
	}
}
