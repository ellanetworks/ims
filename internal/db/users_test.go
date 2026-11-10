package db

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func createTestUser(t *testing.T, d *DB, email string, role Role) int64 {
	t.Helper()

	id, err := d.CreateUser(context.Background(), NewUser{Email: email, Role: role, PasswordHash: "hash-" + email}, 100)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	return id
}

func TestUserLifecycle(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	if n, err := d.CountUsers(ctx); err != nil || n != 0 {
		t.Fatalf("CountUsers = %d, %v, want 0", n, err)
	}

	createTestUser(t, d, "first@example.com", RoleAdmin)

	id := createTestUser(t, d, "admin@example.com", RoleAdmin)
	want := User{ID: id, Email: "admin@example.com", Role: RoleAdmin, PasswordHash: "hash-admin@example.com"}

	for name, get := range map[string]func() (User, error){
		"by email":         func() (User, error) { return d.GetUser(ctx, "admin@example.com") },
		"by email in caps": func() (User, error) { return d.GetUser(ctx, "Admin@Example.COM") },
		"by ID":            func() (User, error) { return d.GetUserByID(ctx, id) },
	} {
		got, err := get()
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v, want %+v", name, got, err, want)
		}
	}

	if err := d.UpdateUserRole(ctx, id, RoleReadOnly, false); err != nil {
		t.Fatalf("UpdateUserRole: %v", err)
	}

	if err := d.UpdateUserPassword(ctx, id, "new-hash", nil); err != nil {
		t.Fatalf("UpdateUserPassword: %v", err)
	}

	want.Role, want.PasswordHash = RoleReadOnly, "new-hash"

	if got, err := d.GetUserByID(ctx, id); err != nil || got != want {
		t.Fatalf("after updates: got %+v, %v, want %+v", got, err, want)
	}

	if n, err := d.CountUsers(ctx); err != nil || n != 2 {
		t.Fatalf("CountUsers = %d, %v, want 2", n, err)
	}

	if err := d.DeleteUser(ctx, id); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	if _, err := d.GetUserByID(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUserByID after delete: %v, want ErrNotFound", err)
	}
}

func TestUserNotFound(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	for name, call := range map[string]func() error{
		"GetUser":            func() error { _, err := d.GetUser(ctx, "nobody@example.com"); return err },
		"GetUserByID":        func() error { _, err := d.GetUserByID(ctx, 42); return err },
		"UpdateUserRole":     func() error { return d.UpdateUserRole(ctx, 42, RoleAdmin, false) },
		"UpdateUserPassword": func() error { return d.UpdateUserPassword(ctx, 42, "hash", nil) },
		"DeleteUser":         func() error { return d.DeleteUser(ctx, 42) },
	} {
		if err := call(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}
}

func TestUserEmailIsUniqueWhateverItsCase(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	createTestUser(t, d, "ota@example.com", RoleNetworkManager)

	_, err := d.CreateUser(ctx, NewUser{Email: "OTA@example.com", Role: RoleAdmin, PasswordHash: "hash"}, 100)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("CreateUser with the same email: %v, want ErrDuplicate", err)
	}
}

func TestUserRoleIsChecked(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	for _, role := range []Role{0, 4} {
		if _, err := d.CreateUser(ctx, NewUser{Email: "x@example.com", Role: role, PasswordHash: "hash"}, 100); err == nil {
			t.Errorf("CreateUser with role %d: no error", role)
		}
	}

	id := createTestUser(t, d, "x@example.com", RoleAdmin)

	if err := d.UpdateUserRole(ctx, id, 4, false); err == nil {
		t.Error("UpdateUserRole to role 4: no error")
	}
}

func TestListUsers(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	ids := []int64{
		createTestUser(t, d, "a@example.com", RoleAdmin),
		createTestUser(t, d, "b@example.com", RoleReadOnly),
		createTestUser(t, d, "c@example.com", RoleNetworkManager),
	}

	tests := map[string]struct {
		page, perPage int
		want          []int64
	}{
		"all oldest first": {1, 10, ids},
		"second page":      {2, 2, ids[2:]},
		"past the end":     {3, 2, []int64{}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			users, total, err := d.ListUsers(ctx, tc.page, tc.perPage)
			if err != nil {
				t.Fatalf("ListUsers: %v", err)
			}

			got := []int64{}
			for _, u := range users {
				got = append(got, u.ID)
			}

			if !reflect.DeepEqual(got, tc.want) || total != len(ids) {
				t.Fatalf("ids = %v (total %d), want %v (total %d)", got, total, tc.want, len(ids))
			}
		})
	}
}

func TestUserUpdatesEndSessions(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	id := createTestUser(t, d, "nm@example.com", RoleNetworkManager)
	other := createTestUser(t, d, "other@example.com", RoleNetworkManager)

	login := func(userID int64, token string) []byte {
		hash := []byte(token)
		if _, err := d.CreateSession(ctx, NewSession{
			UserID: userID, TokenHash: hash, Method: SessionPassword, CreatedAt: now,
			ExpiresAt: now.Add(time.Hour),
		}, 10); err != nil {
			t.Fatal(err)
		}

		return hash
	}

	exists := func(hash []byte) bool {
		_, err := d.GetSessionByTokenHash(ctx, hash)
		if err != nil && !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}

		return err == nil
	}

	current, stale, others := login(id, "current"), login(id, "stale"), login(other, "others")

	if err := d.UpdateUserPassword(ctx, id, "new-hash", current); err != nil {
		t.Fatal(err)
	}

	if !exists(current) || exists(stale) || !exists(others) {
		t.Fatalf("after a password change keeping the current session: current %t, stale %t, others %t",
			exists(current), exists(stale), exists(others))
	}

	if err := d.UpdateUserRole(ctx, id, RoleReadOnly, false); err != nil {
		t.Fatal(err)
	}

	if exists(current) || !exists(others) {
		t.Fatalf("after a role change: current %t, others %t", exists(current), exists(others))
	}

	login(id, "again")

	if err := d.UpdateUserPassword(ctx, id, "newer-hash", nil); err != nil {
		t.Fatal(err)
	}

	if exists([]byte("again")) {
		t.Fatal("a password change keeping no session left one")
	}
}

func TestDeleteUserDeletesSessionsAndTokens(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	id := createTestUser(t, d, "ota@example.com", RoleNetworkManager)

	if _, err := d.CreateSession(ctx, NewSession{
		UserID: id, TokenHash: []byte("session"), Method: SessionPassword, CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}, 10); err != nil {
		t.Fatal(err)
	}

	if _, err := d.CreateAPIToken(ctx, NewAPIToken{
		TokenID: "abc", Name: "ota", TokenHash: []byte("hash"),
		UserID: id, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteUser(ctx, id); err != nil {
		t.Fatal(err)
	}

	if _, err := d.GetSessionByTokenHash(ctx, []byte("session")); !errors.Is(err, ErrNotFound) {
		t.Errorf("session after deleting its user: %v, want ErrNotFound", err)
	}

	if _, err := d.GetAPITokenByTokenID(ctx, "abc"); !errors.Is(err, ErrNotFound) {
		t.Errorf("API token after deleting its user: %v, want ErrNotFound", err)
	}
}

func TestUserWithoutPassword(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	createTestUser(t, d, "admin@example.com", RoleAdmin)

	id, err := d.CreateUser(ctx, NewUser{Email: "sso@example.com", Role: RoleAdmin}, 100)
	if err != nil {
		t.Fatalf("CreateUser without a password: %v", err)
	}

	want := User{ID: id, Email: "sso@example.com", Role: RoleAdmin}
	if got, err := d.GetUserByID(ctx, id); err != nil || got != want {
		t.Fatalf("GetUserByID = %+v, %v, want %+v", got, err, want)
	}

	if err := d.UpdateUserPassword(ctx, id, "hash", nil); err != nil {
		t.Fatalf("UpdateUserPassword: %v", err)
	}

	if err := d.UpdateUserPassword(ctx, id, "", nil); err != nil {
		t.Fatalf("UpdateUserPassword to none: %v", err)
	}

	if err := d.UpdateUserPassword(ctx, id, "", nil); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("UpdateUserPassword to none without a password: %v, want ErrNoPassword", err)
	}

	if err := d.UpdateUserPassword(ctx, id+100, "", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateUserPassword to none of an unknown user: %v, want ErrNotFound", err)
	}

	if got, err := d.GetUserByID(ctx, id); err != nil || got != want {
		t.Fatalf("after removing the password: %+v, %v, want %+v", got, err, want)
	}

	var nulls int
	if err := d.writer.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE password_hash IS NULL`).Scan(&nulls); err != nil || nulls != 1 {
		t.Fatalf("users with a NULL password hash = %d, %v, want 1", nulls, err)
	}
}

func TestCreateUserIsCapped(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	if _, err := d.CreateUser(ctx, NewUser{Email: "a@example.com", Role: RoleReadOnly, RolePinned: true}, 2); err != nil {
		t.Fatal(err)
	}

	if u, err := d.GetUser(ctx, "a@example.com"); err != nil || !u.RolePinned {
		t.Fatalf("user created pinned = %+v, %v", u, err)
	}

	if _, err := d.CreateUser(ctx, NewUser{Email: "b@example.com", Role: RoleReadOnly}, 2); err != nil {
		t.Fatal(err)
	}

	if _, err := d.CreateUser(ctx, NewUser{Email: "c@example.com", Role: RoleReadOnly}, 2); !errors.Is(err, ErrTooManyUsers) {
		t.Fatalf("CreateUser past the limit: %v, want ErrTooManyUsers", err)
	}

	if n, err := d.CountUsers(ctx); err != nil || n != 2 {
		t.Fatalf("CountUsers = %d, %v, want 2", n, err)
	}
}

func TestUserRolePinned(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	id := createTestUser(t, d, "a@example.com", RoleReadOnly)

	if _, err := d.CreateSession(ctx, NewSession{
		UserID: id, TokenHash: []byte("session"), Method: SessionPassword, CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}, 10); err != nil {
		t.Fatal(err)
	}

	for _, pinned := range []bool{true, false} {
		if err := d.UpdateUserRole(ctx, id, RoleReadOnly, pinned); err != nil {
			t.Fatalf("UpdateUserRole pinned %t: %v", pinned, err)
		}

		if u, err := d.GetUserByID(ctx, id); err != nil || u.RolePinned != pinned || u.Role != RoleReadOnly {
			t.Fatalf("after UpdateUserRole pinned %t: %+v, %v", pinned, u, err)
		}
	}

	if _, err := d.GetSessionByTokenHash(ctx, []byte("session")); err != nil {
		t.Fatalf("session after pinning the same role: %v, want kept", err)
	}
}

func TestLastAdminWithAPasswordIsKept(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	admin := createTestUser(t, d, "admin@example.com", RoleAdmin)
	nm := createTestUser(t, d, "nm@example.com", RoleNetworkManager)
	ro := createTestUser(t, d, "ro@example.com", RoleReadOnly)

	sso, err := d.CreateUser(ctx, NewUser{Email: "sso@example.com", Role: RoleAdmin}, 100)
	if err != nil {
		t.Fatal(err)
	}

	sso2, err := d.CreateUser(ctx, NewUser{Email: "sso2@example.com", Role: RoleAdmin}, 100)
	if err != nil {
		t.Fatal(err)
	}

	refused := map[string]func() error{
		"DeleteUser":                 func() error { return d.DeleteUser(ctx, admin) },
		"UpdateUserRole":             func() error { return d.UpdateUserRole(ctx, admin, RoleReadOnly, false) },
		"UpdateUserPassword to none": func() error { return d.UpdateUserPassword(ctx, admin, "", nil) },
	}

	for name, call := range refused {
		if err := call(); !errors.Is(err, ErrLastPasswordAdmin) {
			t.Errorf("%s of the last admin with a password: %v, want ErrLastPasswordAdmin", name, err)
		}
	}

	if u, err := d.GetUserByID(ctx, admin); err != nil || u.Role != RoleAdmin || u.PasswordHash == "" {
		t.Fatalf("after refused changes: %+v, %v", u, err)
	}

	for name, call := range map[string]func() error{
		"UpdateUserRole of the admin to admin":      func() error { return d.UpdateUserRole(ctx, admin, RoleAdmin, false) },
		"UpdateUserPassword of the admin":           func() error { return d.UpdateUserPassword(ctx, admin, "other", nil) },
		"UpdateUserPassword to none of a non-admin": func() error { return d.UpdateUserPassword(ctx, nm, "", nil) },
		"UpdateUserRole of an admin without one":    func() error { return d.UpdateUserRole(ctx, sso, RoleReadOnly, false) },
		"DeleteUser of a user without one":          func() error { return d.DeleteUser(ctx, sso2) },
		"DeleteUser of a non-admin":                 func() error { return d.DeleteUser(ctx, ro) },
	} {
		if err := call(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	if err := d.DeleteUser(ctx, admin+100); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteUser of an unknown user: %v, want ErrNotFound", err)
	}

	second := createTestUser(t, d, "second@example.com", RoleAdmin)

	if err := d.UpdateUserRole(ctx, admin, RoleReadOnly, false); err != nil {
		t.Fatalf("UpdateUserRole with another admin with a password: %v", err)
	}

	if err := d.DeleteUser(ctx, second); !errors.Is(err, ErrLastPasswordAdmin) {
		t.Fatalf("DeleteUser of the new last admin with a password: %v, want ErrLastPasswordAdmin", err)
	}
}
