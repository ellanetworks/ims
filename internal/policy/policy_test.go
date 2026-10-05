package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestEventKindsLogByName(t *testing.T) {
	kinds := []EventKind{EventBearerLost, EventChargingCorrelation}

	var text bytes.Buffer

	slog.New(slog.NewTextHandler(&text, nil)).Info("x", slog.Any("events", kinds))

	if !strings.Contains(text.String(), "BEARER_LOST") || !strings.Contains(text.String(), "CHARGING_CORRELATION") {
		t.Errorf("text log %q, want the event names", text.String())
	}

	b, err := json.Marshal(kinds)
	if err != nil || string(b) != `["BEARER_LOST","CHARGING_CORRELATION"]` {
		t.Errorf("JSON %s, %v; want the event names", b, err)
	}
}

func TestError(t *testing.T) {
	cause := errors.New("cause")

	if err := (&Error{Kind: ErrRefused}); err.Error() != ErrRefused.Error() || !errors.Is(err, ErrRefused) {
		t.Errorf("error without a cause: %q", err)
	}

	err := &Error{Kind: ErrUnreachable, Err: cause}
	if err.Error() != "cause" || !errors.Is(err, ErrUnreachable) || !errors.Is(err, cause) || errors.Is(err, ErrRefused) {
		t.Errorf("error %q does not unwrap to its kind and cause", err)
	}
}
