package db

import (
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/ellanetworks/ims/internal/settings"
)

func TestDefaultSettings(t *testing.T) {
	d := openTestDB(t)

	got, err := d.GetSettings(t.Context())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	want := settings.Operator{
		MCC: "001", MNC: "01",
		Numbering: settings.Numbering{CountryCode: "1", NationalPrefix: "1", InternationalPrefix: "011"},
	}
	if got.Operator != want {
		t.Fatalf("operator = %+v, want %+v", got.Operator, want)
	}

	if len(got.Peers) != 0 || got.Policy != (settings.Policy{Interface: settings.PolicyNone}) {
		t.Fatalf("peers and policy = %+v, %+v; want none", got.Peers, got.Policy)
	}

	if got.CallRecords != (settings.CallRecords{RetentionDays: 90}) {
		t.Fatalf("call records = %+v, want a retention of 90 days", got.CallRecords)
	}

	if err := got.Validate(); err != nil {
		t.Fatalf("the default settings are invalid: %v", err)
	}
}

func TestUpdateOperator(t *testing.T) {
	d := openTestDB(t)

	o := settings.Operator{
		MCC: "208", MNC: "10",
		Numbering: settings.Numbering{CountryCode: "33", NationalPrefix: "0", InternationalPrefix: "00"},
	}

	if err := d.UpdateOperator(t.Context(), o); err != nil {
		t.Fatalf("UpdateOperator: %v", err)
	}

	got, err := d.GetSettings(t.Context())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	if got.Operator != o {
		t.Fatalf("operator = %+v, want %+v", got.Operator, o)
	}
}

func TestPeers(t *testing.T) {
	d := openTestDB(t)

	hss := settings.Peer{
		ID: "0192a000-0000-7000-8000-000000000001", Host: "hss.example.org",
		Address: netip.MustParseAddr("2001:db8::10"), Port: 3868, Transport: settings.TransportTCP,
		Applications: []settings.Application{settings.ApplicationCx}, Priority: 10,
	}
	pcrf := settings.Peer{
		ID: "0192a000-0000-7000-8000-000000000002", Host: "pcrf.example.org",
		Address: netip.MustParseAddr("10.0.0.11"), Port: 3869, Transport: settings.TransportSCTP,
		Applications: []settings.Application{settings.ApplicationCx, settings.ApplicationRx}, Priority: 0,
	}

	for _, p := range []settings.Peer{pcrf, hss} {
		if err := d.CreatePeer(t.Context(), p); err != nil {
			t.Fatalf("CreatePeer: %v", err)
		}
	}

	pcrf.Applications = []settings.Application{settings.ApplicationRx}
	pcrf.Priority = 65535

	if err := d.UpdatePeer(t.Context(), pcrf); err != nil {
		t.Fatalf("UpdatePeer: %v", err)
	}

	got, err := d.GetSettings(t.Context())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	if want := []settings.Peer{hss, pcrf}; !reflect.DeepEqual(got.Peers, want) {
		t.Fatalf("peers = %+v, want %+v in the order of their IDs", got.Peers, want)
	}

	if err := d.DeletePeer(t.Context(), hss.ID); err != nil {
		t.Fatalf("DeletePeer: %v", err)
	}

	if err := d.DeletePeer(t.Context(), hss.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeletePeer = %v, want %v", err, ErrNotFound)
	}

	if err := d.UpdatePeer(t.Context(), hss); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdatePeer of a deleted peer = %v, want %v", err, ErrNotFound)
	}
}

func TestPeersWithoutHost(t *testing.T) {
	d := openTestDB(t)

	peer := func(id, host string) settings.Peer {
		return settings.Peer{
			ID: id, Host: host, Address: netip.MustParseAddr("10.0.0.10"), Port: 3868, Transport: settings.TransportTCP,
			Applications: []settings.Application{settings.ApplicationCx}, Priority: 10,
		}
	}

	for _, p := range []settings.Peer{
		peer("0192a000-0000-7000-8000-000000000001", ""),
		peer("0192a000-0000-7000-8000-000000000002", ""),
		peer("0192a000-0000-7000-8000-000000000003", "hss.example.org"),
	} {
		if err := d.CreatePeer(t.Context(), p); err != nil {
			t.Fatalf("CreatePeer(%q): %v", p.Host, err)
		}
	}

	if err := d.CreatePeer(t.Context(), peer("0192a000-0000-7000-8000-000000000004", "HSS.example.org")); err == nil {
		t.Fatal("a second peer with the same host was stored")
	}
}

func TestRoutes(t *testing.T) {
	d := openTestDB(t)

	got, err := d.GetSettings(t.Context())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	want := []settings.Route{{Application: settings.ApplicationCx}, {Application: settings.ApplicationRx}}
	if !reflect.DeepEqual(got.Routes, want) {
		t.Fatalf("routes = %+v, want %+v: the home domain for both", got.Routes, want)
	}

	rx := settings.Route{Application: settings.ApplicationRx, Realm: "epc.example.org"}
	if err := d.UpdateRoute(t.Context(), rx); err != nil {
		t.Fatalf("UpdateRoute: %v", err)
	}

	if got, _ = d.GetSettings(t.Context()); !reflect.DeepEqual(got.Routes, []settings.Route{want[0], rx}) {
		t.Fatalf("routes = %+v after the update", got.Routes)
	}

	if err := d.UpdateRoute(t.Context(), settings.Route{Application: "gx"}); err == nil {
		t.Fatal("UpdateRoute of an unknown application succeeded")
	}
}

func TestUpdatePolicy(t *testing.T) {
	d := openTestDB(t)

	p := settings.Policy{Interface: settings.PolicyN5, PCFURI: "https://pcf.example.org:7777"}
	if err := d.UpdatePolicy(t.Context(), p); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}

	got, err := d.GetSettings(t.Context())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	if got.Policy != p {
		t.Fatalf("policy = %+v, want %+v", got.Policy, p)
	}
}

func TestUpdateCallRecords(t *testing.T) {
	d := openTestDB(t)

	c := settings.CallRecords{RetentionDays: 30}
	if err := d.UpdateCallRecords(t.Context(), c); err != nil {
		t.Fatalf("UpdateCallRecords: %v", err)
	}

	got, err := d.GetSettings(t.Context())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	if got.CallRecords != c {
		t.Fatalf("call records = %+v, want %+v", got.CallRecords, c)
	}
}
