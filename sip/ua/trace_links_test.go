package ua_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ellanetworks/ims/sip/ua"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestRequestOfADialogLinksToItsOrigin(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))

	alice := newPeer(t, "alice", nil, ua.SessionTimer{})
	bob := newPeerWith(t, "bob", func(c *ua.Config) { c.Transaction.TracerProvider = tp })

	as, _ := established(t, alice, bob)

	if err := as.Bye(context.Background(), ua.Change{}, nil); err != nil {
		t.Fatal(err)
	}

	bob.next("ended")

	span := func(name string) sdktrace.ReadOnlySpan {
		deadline := time.Now().Add(time.Second)

		for {
			for _, s := range rec.Ended() {
				if s.Name() == name && s.SpanKind() == trace.SpanKindServer {
					return s
				}
			}

			if time.Now().After(deadline) {
				t.Fatalf("no %s server span", name)
			}

			time.Sleep(time.Millisecond)
		}
	}

	invite, bye := span("INVITE"), span("BYE")

	var links []trace.SpanID
	for _, l := range bye.Links() {
		links = append(links, l.SpanContext.SpanID())
	}

	if !slices.Equal(links, []trace.SpanID{invite.SpanContext().SpanID()}) {
		t.Fatalf("BYE links %v, want the INVITE's %s", links, invite.SpanContext().SpanID())
	}
}
