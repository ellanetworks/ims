package api

import (
	"context"
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

func writeMCPTTSettingsError(ctx context.Context, w http.ResponseWriter, err error, failed string, own func(string) string, logger *slog.Logger) {
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
		writeError(ctx, w, http.StatusInternalServerError, failed, err, logger)
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

	writeError(ctx, w, status, strings.Join(msgs, "; "), nil, logger)
}

func invalidMCPTT(ctx context.Context, w http.ResponseWriter, message string, logger *slog.Logger) {
	writeError(ctx, w, http.StatusBadRequest, message, nil, logger)
}

var mcpttPrefixes = []struct{ from, to string }{
	{"on-network.transmit-time.time-limit", "transmit_time_limit_ms"},
	{"list.entries", "members"},
}

var mcpttNames = map[string]string{
	"mcptt-user-id":               "mcptt_id",
	"user-alias":                  "alias",
	"on-network-hang-timer":       "hang_timer_ms",
	"on-network-maximum-duration": "maximum_duration_ms",
	"on-network-recvonly":         "recvonly",
}

func mcpttMemberField(path string) string {
	if path == "uri" {
		return "mcptt_id"
	}

	return mcpttField(path)
}

var mcpttIndex = regexp.MustCompile(`\[\d+\]$`)

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

const maxMS = math.MaxInt64 / int64(time.Millisecond)

func duration(name string, ms int64) (time.Duration, string) {
	if ms > maxMS || ms < -maxMS {
		return 0, name + ": " + strconv.FormatInt(ms, 10) + " is out of range"
	}

	return time.Duration(ms) * time.Millisecond, ""
}

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

func paged[T any](items []T, page, perPage int) []T {
	start := min((page-1)*perPage, len(items))
	return items[start:min(start+perPage, len(items))]
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}

	return s
}
