package db

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func testOIDC() OIDC {
	return OIDC{
		Enabled:              true,
		ExternalURL:          "https://ims.example.com",
		Issuer:               testIssuer,
		ClientID:             "ims",
		ClientSecret:         "secret",
		DisplayName:          "Keycloak",
		Scopes:               []string{"openid", "email", "profile", "groups"},
		SessionLifetime:      8 * time.Hour,
		TrustUnverifiedEmail: true,
		CACertificate:        "-----BEGIN CERTIFICATE-----\n-----END CERTIFICATE-----\n",
		GroupsClaim:          "groups",
		GroupRoles: []GroupRole{
			{Group: "ims-admins", Role: RoleAdmin},
			{Group: "noc", Role: RoleNetworkManager},
			{Group: "ops", Role: RoleReadOnly},
		},
	}
}

func TestDefaultOIDC(t *testing.T) {
	d := openTestDB(t)

	got, err := d.GetOIDC(context.Background())
	if err != nil {
		t.Fatalf("GetOIDC: %v", err)
	}

	want := OIDC{Scopes: []string{"openid", "email", "profile"}, SessionLifetime: 24 * time.Hour, GroupRoles: []GroupRole{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetOIDC = %+v, want %+v", got, want)
	}
}

func TestUpdateOIDCPersists(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ims.db")

	d, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	want := testOIDC()
	if err := d.UpdateOIDC(ctx, want); err != nil {
		t.Fatalf("UpdateOIDC: %v", err)
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	if d, err = Open(ctx, path); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	if got, err := d.GetOIDC(ctx); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("GetOIDC after reopening = %+v, %v, want %+v", got, err, want)
	}

	want.GroupRoles = []GroupRole{{Group: "ops", Role: RoleReadOnly}, {Group: "ims-admins", Role: RoleAdmin}}
	if err := d.UpdateOIDC(ctx, want); err != nil {
		t.Fatalf("UpdateOIDC with reordered groups: %v", err)
	}

	if got, err := d.GetOIDC(ctx); err != nil || !reflect.DeepEqual(got.GroupRoles, want.GroupRoles) {
		t.Fatalf("GroupRoles = %+v, %v, want %+v", got.GroupRoles, err, want.GroupRoles)
	}
}

func TestUpdateOIDCIsChecked(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	for name, change := range map[string]func(*OIDC){
		"enabled without an external URL":   func(o *OIDC) { o.ExternalURL = "" },
		"enabled without an issuer":         func(o *OIDC) { o.Issuer = "" },
		"enabled without a client ID":       func(o *OIDC) { o.ClientID = "" },
		"enabled without a display name":    func(o *OIDC) { o.DisplayName = "" },
		"enabled without scopes":            func(o *OIDC) { o.Scopes = nil },
		"a session lifetime of zero":        func(o *OIDC) { o.SessionLifetime = 0 },
		"a group mapped twice":              func(o *OIDC) { o.GroupRoles = append(o.GroupRoles, o.GroupRoles[0]) },
		"a group with no name":              func(o *OIDC) { o.GroupRoles[0].Group = "" },
		"a group mapped to an unknown role": func(o *OIDC) { o.GroupRoles[0].Role = 5 },
	} {
		o := testOIDC()
		change(&o)

		if err := d.UpdateOIDC(ctx, o); err == nil {
			t.Errorf("UpdateOIDC with %s: no error", name)
		}
	}

	if got, err := d.GetOIDC(ctx); err != nil || got.Enabled {
		t.Fatalf("after refused updates: Enabled %t, %v, want false", got.Enabled, err)
	}

	if err := d.UpdateOIDC(ctx, OIDC{SessionLifetime: time.Hour}); err != nil {
		t.Fatalf("UpdateOIDC disabled and empty: %v", err)
	}
}

func TestUpdateOIDCEndsOIDCSessions(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	tests := map[string]struct {
		change       func(*OIDC)
		keepSessions bool
	}{
		"another client secret": {func(o *OIDC) { o.ClientSecret = "rotated" }, true},
		"another group mapping": {func(o *OIDC) { o.GroupRoles = nil }, true},
		"disabled":              {func(o *OIDC) { o.Enabled = false }, false},
		"another issuer":        {func(o *OIDC) { o.Issuer = "https://other.example.com" }, false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d := openTestDB(t)

			enableTestOIDC(t, d)

			id := createTestUser(t, d, "a@example.com", RoleReadOnly)

			identityID, err := d.CreateIdentity(ctx, id, NewIdentity{Issuer: testIssuer, Subject: "sub", At: now})
			if err != nil {
				t.Fatal(err)
			}

			for _, s := range []NewSession{
				{TokenHash: []byte("password"), Method: SessionPassword},
				{TokenHash: []byte("oidc"), Method: SessionOIDC, IdentityID: identityID},
			} {
				s.UserID, s.CreatedAt, s.ExpiresAt = id, now, now.Add(time.Hour)
				if _, err := d.CreateSession(ctx, s, 10); err != nil {
					t.Fatal(err)
				}
			}

			o := testOIDC()
			tc.change(&o)

			if err := d.UpdateOIDC(ctx, o); err != nil {
				t.Fatalf("UpdateOIDC: %v", err)
			}

			if _, err := d.GetSessionByTokenHash(ctx, []byte("password")); err != nil {
				t.Errorf("password session: %v, want kept", err)
			}

			_, err = d.GetSessionByTokenHash(ctx, []byte("oidc"))
			if kept := err == nil; kept != tc.keepSessions {
				t.Errorf("OIDC session kept = %t (%v), want %t", kept, err, tc.keepSessions)
			}

			if _, err := d.GetIdentity(ctx, testIssuer, "sub"); err != nil {
				t.Errorf("link: %v, want kept", err)
			}
		})
	}
}

func TestOIDCCookieKeySurvivesReopening(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ims.db")

	var keys [][]byte

	for range 2 {
		d, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}

		key, err := d.OIDCCookieKey(ctx)
		if err != nil {
			t.Fatalf("OIDCCookieKey: %v", err)
		}

		secret, err := d.JWTSecret(ctx)
		if err != nil {
			t.Fatalf("JWTSecret: %v", err)
		}

		if bytes.Equal(key, secret) {
			t.Fatal("the OIDC cookie key is the JWT secret")
		}

		keys = append(keys, key)

		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	}

	if len(keys[0]) != oidcCookieKeyLength || !bytes.Equal(keys[0], keys[1]) {
		t.Fatalf("keys %x and %x, want the same %d bytes", keys[0], keys[1], oidcCookieKeyLength)
	}
}
