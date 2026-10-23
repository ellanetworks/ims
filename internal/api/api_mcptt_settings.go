package api

import (
	"errors"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	mcpttsettings "github.com/ellanetworks/ims/internal/mcptt/settings"
)

// writeMCPTTSettingsError answers a failed change of settings: with its problems for the client's mistakes, each at the
// field of the request body, own of the body's document, or with a generic message if it could not be saved.
func writeMCPTTSettingsError(w http.ResponseWriter, err error, failed string, own func(string) string, logger *slog.Logger) {
	status := http.StatusInternalServerError

	switch {
	case errors.Is(err, mcpttsettings.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, mcpttsettings.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, mcpttsettings.ErrNotFound):
		status = http.StatusNotFound
	}

	var ce *mcpttsettings.ChangeError
	if status == http.StatusInternalServerError || !errors.As(err, &ce) {
		writeError(w, http.StatusInternalServerError, failed, err, logger)
		return
	}

	msgs := make([]string, len(ce.Problems))

	for i, p := range ce.Problems {
		if p.Of == "" {
			p.Path = own(p.Path)
		} else {
			p.Path = mcpttField(p.Path)
		}

		msgs[i] = p.Error()
	}

	writeError(w, status, strings.Join(msgs, "; "), nil, logger)
}

// invalidMCPTT answers a request body invalid by itself.
func invalidMCPTT(w http.ResponseWriter, message string, logger *slog.Logger) {
	writeError(w, http.StatusBadRequest, message, nil, logger)
}

// mcpttPrefixes are the paths of the settings that are another path of the request bodies, longest first.
var mcpttPrefixes = []struct{ from, to string }{
	{"on-network.fc-timers-counters.", "fc_timers_counters."},
	{"on-network.transmit-time.time-limit", "transmit_time_limit_ms"},
	{"on-network.num-levels-priority-hierarchy", "num_levels_priority_hierarchy"},
	{"list.entries", "members"},
}

// mcpttNames are the keys of the settings that are another field of the request bodies; the others are those of the
// keys, in snake case.
var mcpttNames = map[string]string{
	"mcptt-user-id":                    "mcptt_id",
	"T1-end-of-rtp-media":              "t1_end_of_rtp_media_ms",
	"T3-stop-talking-grace":            "t3_stop_talking_grace_ms",
	"T7-floor-idle":                    "t7_floor_idle_ms",
	"T8-floor-revoke":                  "t8_floor_revoke_ms",
	"C7-floor-idle":                    "c7_floor_idle",
	"on-network-max-participant-count": "max_participant_count",
	"on-network-hang-timer":            "hang_timer_ms",
	"on-network-maximum-duration":      "maximum_duration_ms",
	"on-network-timeout-for-acknowledgement-of-required-members": "timeout_for_acknowledgement_of_required_members_ms",
	"on-network-required":                      "required",
	"on-network-recvonly":                      "recvonly",
	"on-network-affiliation-to-group-required": "affiliation_required",
}

// mcpttMemberField is the field of a member's request body of the path of a settings.FieldError of the member.
func mcpttMemberField(path string) string {
	if path == "uri" {
		return "mcptt_id"
	}

	return mcpttField(path)
}

var mcpttIndex = regexp.MustCompile(`\[\d+\]$`)

// mcpttField is the field of a request body of the path of a settings.FieldError: members[2].user_priority of
// list.entries[2].user-priority. The uri of a group is its group_id, that of a member its mcptt_id.
func mcpttField(path string) string {
	for _, p := range mcpttPrefixes {
		if rest, ok := strings.CutPrefix(path, p.from); ok {
			path = p.to + rest
			break
		}
	}

	if path == "" {
		return ""
	}

	segs := strings.Split(path, ".")

	for i, s := range segs {
		n := mcpttIndex.FindString(s)
		name := strings.TrimSuffix(s, n)

		switch to, ok := mcpttNames[name]; {
		case ok:
			name = to
		case name == "uri" && i == 0:
			name = "group_id"
		case name == "uri":
			name = "mcptt_id"
		default:
			name = strings.TrimPrefix(strings.ReplaceAll(name, "-", "_"), "on_network_")
		}

		segs[i] = name + n
	}

	return strings.Join(segs, ".")
}

// maxMS is the most milliseconds a duration holds.
const maxMS = math.MaxInt64 / int64(time.Millisecond)

// duration is the duration of ms milliseconds of the field name, or the problem with it.
func duration(name string, ms int64) (time.Duration, string) {
	if ms > maxMS || ms < -maxMS {
		return 0, name + ": " + strconv.FormatInt(ms, 10) + " is out of range"
	}

	return time.Duration(ms) * time.Millisecond, ""
}

// optionalDuration is duration, of an optional field: nil if absent.
func optionalDuration(name string, ms *int64) (*time.Duration, string) {
	if ms == nil {
		return nil, ""
	}

	d, problem := duration(name, *ms)

	return &d, problem
}

func milliseconds(d time.Duration) int64 { return d.Milliseconds() }

func optionalMilliseconds(d *time.Duration) *int64 {
	if d == nil {
		return nil
	}

	ms := d.Milliseconds()

	return &ms
}

// paged are the items of page of perPage of items.
func paged[T any](items []T, page, perPage int) []T {
	start := min((page-1)*perPage, len(items))
	return items[start:min(start+perPage, len(items))]
}

// orEmpty is s, empty rather than nil, for a JSON array.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}

	return s
}
