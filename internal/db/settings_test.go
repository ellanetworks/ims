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
		ID: "0192a000-0000-7000-8000-000000000001", Host: "hss.example.org", Realm: "example.org",
		Address: netip.MustParseAddr("2001:db8::10"), Port: 3868, Transport: settings.TransportTCP,
		Applications: []settings.Application{settings.ApplicationCx},
	}
	pcrf := settings.Peer{
		ID: "0192a000-0000-7000-8000-000000000002", Host: "pcrf.example.org", Realm: "example.org",
		Address: netip.MustParseAddr("10.0.0.11"), Port: 3869, Transport: settings.TransportSCTP,
		Applications: []settings.Application{settings.ApplicationCx, settings.ApplicationRx},
	}

	for _, p := range []settings.Peer{pcrf, hss} {
		if err := d.CreatePeer(t.Context(), p); err != nil {
			t.Fatalf("CreatePeer: %v", err)
		}
	}

	pcrf.Applications = []settings.Application{settings.ApplicationRx}
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
