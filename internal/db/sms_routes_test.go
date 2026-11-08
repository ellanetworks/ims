package db

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ellanetworks/ims/internal/settings"
	"github.com/mattn/go-sqlite3"
)

func testSMSCs(t *testing.T, d *DB) (settings.Peer, settings.Peer) {
	t.Helper()

	smsc := settings.Peer{
		ID: "0192a000-0000-7000-8000-000000000041", Host: "smsc.example.org", Address: "10.0.0.12", Port: 3868,
		Transport: settings.TransportSCTP, Applications: []settings.Application{settings.ApplicationSGd}, Priority: 10,
	}
	backup := smsc
	backup.ID, backup.Host, backup.Address = "0192a000-0000-7000-8000-000000000042", "backup.example.org", "10.0.0.13"

	for _, p := range []settings.Peer{smsc, backup} {
		if err := d.CreatePeer(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}

	return smsc, backup
}

func TestSMSRoutes(t *testing.T) {
	d := openTestDB(t)
	smsc, backup := testSMSCs(t, d)

	def := settings.SMSRoute{ID: "0192a000-0000-7000-8000-000000000051", Peers: []string{smsc.ID}}
	sc := settings.SMSRoute{
		ID: "0192a000-0000-7000-8000-000000000052", ServiceCentre: "15550000000", Peers: []string{backup.ID},
	}

	for _, r := range []settings.SMSRoute{sc, def} {
		if err := d.CreateSMSRoute(t.Context(), r); err != nil {
			t.Fatalf("CreateSMSRoute: %v", err)
		}
	}

	if err := d.CreateSMSRoute(t.Context(), settings.SMSRoute{
		ID: "0192a000-0000-7000-8000-000000000053", ServiceCentre: "15550000000", Peers: []string{smsc.ID},
	}); err == nil {
		t.Fatal("a second route of service centre 15550000000 was stored")
	}

	sc.ServiceCentre = "15550000001"
	sc.Peers = []string{smsc.ID, backup.ID}

	if err := d.UpdateSMSRoute(t.Context(), sc); err != nil {
		t.Fatalf("UpdateSMSRoute: %v", err)
	}

	got, err := d.GetSettings(t.Context())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	if want := []settings.SMSRoute{def, sc}; !reflect.DeepEqual(got.SMSRoutes, want) {
		t.Fatalf("routes = %+v, want %+v", got.SMSRoutes, want)
	}

	if err := d.DeleteSMSRoute(t.Context(), def.ID); err != nil {
		t.Fatalf("DeleteSMSRoute: %v", err)
	}

	if err := d.DeleteSMSRoute(t.Context(), def.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteSMSRoute = %v, want %v", err, ErrNotFound)
	}

	if err := d.UpdateSMSRoute(t.Context(), def); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateSMSRoute of a deleted route = %v, want %v", err, ErrNotFound)
	}

	if err := d.UpdateSMSRoute(t.Context(), settings.SMSRoute{
		ID: sc.ID, ServiceCentre: "15550000002", Peers: []string{"0192"},
	}); err == nil {
		t.Fatal("a route to an unknown peer was stored")
	}

	if got, err := d.GetSettings(t.Context()); err != nil || !reflect.DeepEqual(got.SMSRoutes, []settings.SMSRoute{sc}) {
		t.Fatalf("routes = %+v (%v), want %+v unchanged by the failed update", got.SMSRoutes, err, sc)
	}
}

func TestSMSRoutePeerForeignKey(t *testing.T) {
	d := openTestDB(t)
	smsc, _ := testSMSCs(t, d)

	route := settings.SMSRoute{ID: "0192a000-0000-7000-8000-000000000061", Peers: []string{smsc.ID}}
	if err := d.CreateSMSRoute(t.Context(), route); err != nil {
		t.Fatal(err)
	}

	if err := d.DeletePeer(t.Context(), smsc.ID); !isConstraint(err, sqlite3.ErrConstraintForeignKey) {
		t.Fatalf("DeletePeer in use = %v, want a foreign key failure", err)
	}

	if err := d.DeleteSMSRoute(t.Context(), route.ID); err != nil {
		t.Fatal(err)
	}

	if err := d.DeletePeer(t.Context(), smsc.ID); err != nil {
		t.Fatalf("DeletePeer no longer in use: %v", err)
	}
}
