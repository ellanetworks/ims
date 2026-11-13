package db

import (
	"context"
	"errors"
	"fmt"
)

var ErrUpgradePending = fmt.Errorf("%w: the cluster has not been upgraded to support this yet", ErrBusy)

var minSchema = map[CommandType]int{}

func commandSchema(t CommandType) int {
	if s, ok := minSchema[t]; ok {
		return s
	}

	return baselineSchema
}

type migrate struct {
	Target int `json:"target"`
}

func (d *DB) Migrate(ctx context.Context, target int) (err error) {
	ctx, done := d.observe(ctx, "Migrate", poolWrite)
	defer done(&err)

	_, err = submit[struct{}](ctx, d, cmdMigrate, migrate{Target: target})

	return err
}

func applyMigrate(a *applyCtx, p migrate) (struct{}, error) {
	var current int

	if err := a.tx.QueryRowContext(a.ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return struct{}{}, err
	}

	switch {
	case p.Target <= current:
		return struct{}{}, nil
	case p.Target != current+1:
		return struct{}{}, fmt.Errorf("%w: migration to schema %d from %d", ErrInvalid, p.Target, current)
	case p.Target > len(migrations):
		return struct{}{}, errors.New("the log needs a newer binary: schema " + fmt.Sprint(p.Target))
	}

	if _, err := a.tx.ExecContext(a.ctx, migrations[current]); err != nil {
		return struct{}{}, fmt.Errorf("migration %d: %w", p.Target, err)
	}

	if _, err := a.tx.ExecContext(a.ctx, fmt.Sprintf("PRAGMA user_version = %d", p.Target)); err != nil {
		return struct{}{}, err
	}

	a.events.Migrated = p.Target

	return struct{}{}, nil
}
