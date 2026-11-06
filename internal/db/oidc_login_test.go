package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

var loginTime = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func testLogin(subject, email string) OIDCLogin {
	o := testOIDC()

	return OIDCLogin{
		Issuer: o.Issuer, ClientID: o.ClientID, Subject: subject, Email: email, LinkEmail: email, MaxUsers: 100,
		Session: NewSession{
			TokenHash: []byte("session of " + subject + " at " + fmt.Sprint(time.Now().UnixNano())),
			CreatedAt: loginTime, ExpiresAt: loginTime.Add(time.Hour),
		},
		MaxSessionsPerUser: 10,
	}
}

func TestLogInWithOIDCLinksThenMatchesTheSubject(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	enableTestOIDC(t, d)

	id := createTestUser(t, d, "nm@example.com", RoleNetworkManager)

	l := testLogin("sub-nm", "NM@example.com")
	l.LinkEmail = "nm@example.com"

	res, err := d.LogInWithOIDC(ctx, l)
	if err != nil {
		t.Fatalf("first login: %v", err)
	}

	if !res.Linked || res.Created || res.User.ID != id || !res.User.HasIdentity || res.RoleChanged() {
		t.Fatalf("first login = %+v", res)
	}

	s, err := d.GetSession(ctx, res.SessionID)
	if err != nil || s.Method != SessionOIDC || s.IdentityID != res.IdentityID || s.UserID != id {
		t.Fatalf("session = %+v, %v", s, err)
	}

	l = testLogin("sub-nm", "renamed@example.com")
	l.LinkEmail = ""
	l.Session.CreatedAt = loginTime.Add(time.Minute)

	res, err = d.LogInWithOIDC(ctx, l)
	if err != nil || res.Linked || res.User.ID != id {
		t.Fatalf("returning login = %+v, %v", res, err)
	}

	i, err := d.GetIdentity(ctx, testIssuer, "sub-nm")
	if err != nil || i.Email != "renamed@example.com" || !i.LastLoginAt.Equal(loginTime.Add(time.Minute)) ||
		!i.CreatedAt.Equal(loginTime) {
		t.Fatalf("identity after the returning login = %+v, %v", i, err)
	}

	if _, err := d.LogInWithOIDC(ctx, testLogin("sub-other", "nm@example.com")); !errors.Is(err, ErrLinkedElsewhere) {
		t.Fatalf("another subject with the email: %v, want ErrLinkedElsewhere", err)
	}
}

func TestLogInWithOIDCRefusals(t *testing.T) {
	ctx := context.Background()

	tests := map[string]struct {
		change func(*OIDCLogin)
		want   error
	}{
		"OIDC disabled":                   {func(*OIDCLogin) {}, ErrOIDCDisabled},
		"another issuer":                  {func(l *OIDCLogin) { l.Issuer = "https://other.example.com" }, ErrOIDCChanged},
		"another client":                  {func(l *OIDCLogin) { l.ClientID = "other" }, ErrOIDCChanged},
		"an untrusted email":              {func(l *OIDCLogin) { l.LinkEmail = "" }, ErrNotLinked},
		"an unknown email without groups": {func(l *OIDCLogin) { l.LinkEmail = "nobody@example.com" }, ErrNoUser},
		"an unknown email and no group": {func(l *OIDCLogin) {
			l.LinkEmail, l.MapsGroups = "nobody@example.com", true
		}, ErrNoRole},
		"an unknown email past the user limit": {func(l *OIDCLogin) {
			l.LinkEmail, l.MapsGroups, l.Role, l.MaxUsers = "nobody@example.com", true, RoleReadOnly, 1
		}, ErrTooManyUsers},
		"a known email and no group": {func(l *OIDCLogin) { l.MapsGroups = true }, ErrNoRole},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d := openTestDB(t)

			if tc.want != ErrOIDCDisabled {
				enableTestOIDC(t, d)
			}

			createTestUser(t, d, "nm@example.com", RoleNetworkManager)

			l := testLogin("sub-nm", "nm@example.com")
			tc.change(&l)

			if _, err := d.LogInWithOIDC(ctx, l); !errors.Is(err, tc.want) {
				t.Fatalf("LogInWithOIDC: %v, want %v", err, tc.want)
			}

			if _, err := d.GetIdentity(ctx, testIssuer, "sub-nm"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("identity after a refused login: %v, want none", err)
			}

			if n, err := d.CountUsers(ctx); err != nil || n != 1 {
				t.Fatalf("CountUsers = %d, %v, want 1", n, err)
			}

			var sessions int
			if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 0 {
				t.Fatalf("sessions = %d, %v, want 0", sessions, err)
			}
		})
	}
}

func TestLogInWithOIDCCreatesAUser(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	enableTestOIDC(t, d)

	l := testLogin("sub-new", "new@example.com")
	l.MapsGroups, l.Role = true, RoleReadOnly

	res, err := d.LogInWithOIDC(ctx, l)
	if err != nil || !res.Created || res.Linked || res.RoleChanged() {
		t.Fatalf("LogInWithOIDC = %+v, %v", res, err)
	}

	want := User{ID: res.User.ID, Email: "new@example.com", Role: RoleReadOnly, HasIdentity: true}
	if got, err := d.GetUserByID(ctx, res.User.ID); err != nil || got != want || res.User != want {
		t.Fatalf("user = %+v, %v; result %+v; want %+v", got, err, res.User, want)
	}
}

func TestLogInWithOIDCSyncsTheRole(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	enableTestOIDC(t, d)

	createTestUser(t, d, "admin@example.com", RoleAdmin)
	id := createTestUser(t, d, "nm@example.com", RoleNetworkManager)

	if _, err := d.CreateSession(ctx, NewSession{
		UserID: id, TokenHash: []byte("old"), Method: SessionPassword, CreatedAt: loginTime,
		ExpiresAt: loginTime.Add(time.Hour),
	}, 10); err != nil {
		t.Fatal(err)
	}

	l := testLogin("sub-nm", "nm@example.com")
	l.MapsGroups, l.Role = true, RoleReadOnly

	res, err := d.LogInWithOIDC(ctx, l)
	if err != nil || !res.RoleChanged() || res.PreviousRole != RoleNetworkManager || res.User.Role != RoleReadOnly {
		t.Fatalf("LogInWithOIDC = %+v, %v", res, err)
	}

	if _, err := d.GetSessionByTokenHash(ctx, []byte("old")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session from before the role change: %v, want ended", err)
	}

	if _, err := d.GetSession(ctx, res.SessionID); err != nil {
		t.Fatalf("new session: %v", err)
	}

	if err := d.UpdateUserRole(ctx, id, RoleReadOnly, true); err != nil {
		t.Fatal(err)
	}

	l = testLogin("sub-nm", "nm@example.com")
	l.MapsGroups = true

	if res, err := d.LogInWithOIDC(ctx, l); err != nil || res.RoleChanged() || res.User.Role != RoleReadOnly {
		t.Fatalf("login of a pinned user with no group = %+v, %v", res, err)
	}

	l = testLogin("sub-admin", "admin@example.com")
	l.MapsGroups, l.Role = true, RoleReadOnly

	res, err = d.LogInWithOIDC(ctx, l)
	if err != nil || !res.RoleKept || res.RoleChanged() || res.User.Role != RoleAdmin {
		t.Fatalf("login of the last admin with a password = %+v, %v", res, err)
	}
}

func TestLogInWithOIDCIsSerialized(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	enableTestOIDC(t, d)

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
		ids  = map[int64]bool{}
	)

	for i := range 8 {
		wg.Go(func() {
			l := testLogin("sub-new", "new@example.com")
			l.MapsGroups, l.Role = true, RoleReadOnly
			l.Session.TokenHash = fmt.Appendf(nil, "session %d", i)

			res, err := d.LogInWithOIDC(ctx, l)

			mu.Lock()
			defer mu.Unlock()

			errs = append(errs, err)
			ids[res.User.ID] = true
		})
	}

	wg.Wait()

	if err := errors.Join(errs...); err != nil || len(ids) != 1 {
		t.Fatalf("concurrent first logins: users %v, errors %v", ids, err)
	}
}

func TestUseOIDCState(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	state := []byte("0123456789abcdef0123456789abcdef")
	other := []byte("fedcba9876543210fedcba9876543210")

	if err := d.UseOIDCState(ctx, state, loginTime.Add(10*time.Minute), loginTime); err != nil {
		t.Fatalf("first use: %v", err)
	}

	if err := d.UseOIDCState(ctx, state, loginTime.Add(10*time.Minute), loginTime.Add(time.Minute)); !errors.Is(err, ErrStateUsed) {
		t.Fatalf("second use: %v, want ErrStateUsed", err)
	}

	if err := d.UseOIDCState(ctx, other, loginTime.Add(20*time.Minute), loginTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("another state: %v", err)
	}

	var n int
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM oidc_used_states`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("used states after expiry = %d, %v, want 1", n, err)
	}
}
