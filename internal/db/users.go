package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/mattn/go-sqlite3"
)

type Role int

const (
	RoleAdmin          Role = 1
	RoleReadOnly       Role = 2
	RoleNetworkManager Role = 3
)

type User struct {
	ID           int64
	Email        string
	Role         Role
	RolePinned   bool
	PasswordHash string
	HasIdentity  bool
}

type NewUser struct {
	Email        string
	Role         Role
	RolePinned   bool
	PasswordHash string
}

func (d *DB) CountUsers(ctx context.Context) (_ int, err error) {
	defer d.observe(poolRead, &err)()

	var n int
	if err := d.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}

	return n, nil
}

func (d *DB) CreateUser(ctx context.Context, u NewUser, maxUsers int) (id int64, err error) {
	defer d.observe(poolWrite, &err)()

	err = d.inTx(ctx, "create user", func(tx *sql.Tx) error {
		id, err = createUser(ctx, tx, u, maxUsers)
		return err
	})

	return id, err
}

func createUser(ctx context.Context, tx *sql.Tx, u NewUser, maxUsers int) (int64, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, err
	}

	if n >= maxUsers {
		return 0, ErrTooManyUsers
	}

	res, err := tx.ExecContext(ctx, `INSERT INTO users (email, role_id, role_pinned, password_hash) VALUES (?, ?, ?, ?)`,
		u.Email, u.Role, u.RolePinned, nullableString(u.PasswordHash))
	if isConstraint(err, sqlite3.ErrConstraintUnique) {
		return 0, ErrDuplicate
	}

	if err != nil {
		return 0, err
	}

	return res.LastInsertId()
}

func (d *DB) GetUser(ctx context.Context, email string) (_ User, err error) {
	defer d.observe(poolRead, &err)()

	return getUser(d.read.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = ?`, email))
}

func (d *DB) GetUserByID(ctx context.Context, id int64) (_ User, err error) {
	defer d.observe(poolRead, &err)()

	return getUser(d.read.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

func (d *DB) ListUsers(ctx context.Context, page, perPage int) (_ []User, _ int, err error) {
	defer d.observe(poolRead, &err)()

	var total int
	if err := d.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}

	rows, err := d.read.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY id LIMIT ? OFFSET ?`,
		perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}

	defer func() { _ = rows.Close() }()

	users := []User{}

	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("list users: %w", err)
		}

		users = append(users, u)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}

	return users, total, nil
}

func (d *DB) UpdateUserRole(ctx context.Context, id int64, role Role, pinned bool) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "update user role", func(tx *sql.Tx) error {
		var current Role

		err := tx.QueryRowContext(ctx, `SELECT role_id FROM users WHERE id = ?`, id).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}

		if err != nil {
			return err
		}

		if role != RoleAdmin {
			if err := keepPasswordAdmin(ctx, tx, id); err != nil {
				return err
			}
		}

		if _, err := tx.ExecContext(ctx, `UPDATE users SET role_id = ?, role_pinned = ? WHERE id = ?`, role, pinned,
			id); err != nil {
			return err
		}

		if role == current {
			return nil
		}

		_, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)

		return err
	})
}

func (d *DB) UpdateUserPassword(ctx context.Context, id int64, passwordHash string, keepSession []byte) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "update user password", func(tx *sql.Tx) error {
		if passwordHash == "" {
			if err := keepPasswordAdmin(ctx, tx, id); err != nil {
				return err
			}
		}

		res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ? AND (? OR password_hash IS NOT NULL)`,
			nullableString(passwordHash), id, passwordHash != "")
		if err != nil {
			return err
		}

		n, err := res.RowsAffected()
		if err != nil {
			return err
		}

		if n == 0 {
			return missingPassword(ctx, tx, id)
		}

		_, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash IS NOT ?`, id, keepSession)

		return err
	})
}

func missingPassword(ctx context.Context, tx *sql.Tx, id int64) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = ?)`, id).Scan(&exists); err != nil {
		return err
	}

	if exists {
		return ErrNoPassword
	}

	return ErrNotFound
}

func (d *DB) DeleteUser(ctx context.Context, id int64) (err error) {
	defer d.observe(poolWrite, &err)()

	return d.inTx(ctx, "delete user", func(tx *sql.Tx) error {
		if err := keepPasswordAdmin(ctx, tx, id); err != nil {
			return err
		}

		return updateUser(ctx, tx, `DELETE FROM users WHERE id = ?`, id)
	})
}

func keepPasswordAdmin(ctx context.Context, tx *sql.Tx, id int64) error {
	var ok bool
	if err := tx.QueryRowContext(ctx, `SELECT
		NOT EXISTS (SELECT 1 FROM users WHERE id = ? AND role_id = ? AND password_hash IS NOT NULL)
		OR EXISTS (SELECT 1 FROM users WHERE id != ? AND role_id = ? AND password_hash IS NOT NULL)`,
		id, RoleAdmin, id, RoleAdmin).Scan(&ok); err != nil {
		return err
	}

	if !ok {
		return ErrLastPasswordAdmin
	}

	return nil
}

const userColumns = `id, email, role_id, role_pinned, password_hash,
	EXISTS (SELECT 1 FROM user_identities JOIN oidc ON oidc.issuer = user_identities.issuer
		WHERE user_identities.user_id = users.id)`

func getUser(row *sql.Row) (User, error) {
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}

	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}

	return u, nil
}

func scanUser(row scanner) (User, error) {
	var (
		u    User
		hash sql.NullString
	)

	err := row.Scan(&u.ID, &u.Email, &u.Role, &u.RolePinned, &hash, &u.HasIdentity)
	u.PasswordHash = hash.String

	return u, err
}

func updateUser(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}

	return checkAffected(res)
}
