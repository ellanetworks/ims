package cluster

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ellanetworks/ims/internal/db"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestForwardedWriteIsTraced(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))

	c := newClusterWith(t, 2, func(_ int, dir, address string) *env {
		ctx := context.Background()

		d, err := db.Open(ctx, filepath.Join(dir, "ims.db"), db.WithTracerProvider(tp))
		if err != nil {
			t.Fatal(err)
		}

		n, err := Open(ctx, Config{Dir: filepath.Join(dir, "raft"), Address: address, Version: "test", TracerProvider: tp}, d)
		if err != nil {
			_ = d.Close()

			t.Fatal(err)
		}

		d.SetProposer(n)

		return &env{dir: dir, db: d, node: n}
	})

	if err := c.follower().node.peers.call(timeout(t), "/cluster/v1/probe", struct{}{}, nil); err != nil {
		t.Fatal(err)
	}

	ctx, root := tp.Tracer("test").Start(timeout(t), "test")

	if _, err := c.follower().db.CreateUser(ctx, db.NewUser{Email: "a@example.com", Role: db.RoleAdmin, PasswordHash: "h"}, 10); err != nil {
		t.Fatal(err)
	}

	root.End()

	byName := map[string][]sdktrace.ReadOnlySpan{}

	for _, s := range rec.Ended() {
		byName[s.Name()] = append(byName[s.Name()], s)

		if s.SpanKind() != trace.SpanKindInternal && s.SpanContext().TraceID() != root.SpanContext().TraceID() {
			t.Errorf("%s %s outside a trace", s.SpanKind(), s.Name())
		}
	}

	one := func(name string, kind trace.SpanKind) sdktrace.ReadOnlySpan {
		t.Helper()

		var found []sdktrace.ReadOnlySpan

		for _, s := range byName[name] {
			if s.SpanKind() == kind && s.SpanContext().TraceID() == root.SpanContext().TraceID() {
				found = append(found, s)
			}
		}

		if len(found) != 1 {
			t.Fatalf("%d %s %s spans in the trace, want 1", len(found), kind, name)
		}

		return found[0]
	}

	method := one("db.CreateUser", trace.SpanKindInternal)
	client := one("POST /cluster/v1/propose", trace.SpanKindClient)
	server := one("POST /cluster/v1/propose", trace.SpanKindServer)
	wait := one("raft wait applied", trace.SpanKindInternal)

	for _, p := range []struct {
		child, parent sdktrace.ReadOnlySpan
	}{{method, nil}, {client, method}, {wait, method}, {server, client}} {
		want := root.SpanContext().SpanID()
		if p.parent != nil {
			want = p.parent.SpanContext().SpanID()
		}

		if p.child.Parent().SpanID() != want {
			t.Errorf("%s %s is not a child of the span it should be", p.child.SpanKind(), p.child.Name())
		}
	}

	linked := false

	for _, s := range byName["raft apply"] {
		for _, l := range s.Links() {
			if l.SpanContext.SpanID() == server.SpanContext().SpanID() {
				linked = true
			}
		}
	}

	if !linked {
		t.Error("no raft apply span links to the leader's propose span")
	}
}
