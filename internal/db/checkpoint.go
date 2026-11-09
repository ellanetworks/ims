package db

import (
	"context"
	"database/sql"
	"time"
)

const walCheckpointPages = 10000

var checkpointInterval = time.Second

type checkpointer struct {
	conn   *sql.DB
	cancel context.CancelFunc
	done   chan struct{}
}

func startCheckpointer(ctx context.Context, path string, writer *sql.DB) (*checkpointer, error) {
	conn, err := sql.Open("sqlite3", dsn(path)+"?"+dsnParams)
	if err != nil {
		return nil, err
	}

	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)

	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	c := &checkpointer{conn: conn, cancel: cancel, done: make(chan struct{})}

	go c.run(runCtx, writer)

	return c, nil
}

func (c *checkpointer) run(ctx context.Context, writer *sql.DB) {
	defer close(c.done)

	t := time.NewTicker(checkpointInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		if frames, err := checkpoint(ctx, c.conn); err != nil || frames < walCheckpointPages {
			continue
		}

		_, _ = checkpoint(ctx, writer)
	}
}

func checkpoint(ctx context.Context, conn *sql.DB) (int, error) {
	var busy, frames, done int

	err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &frames, &done)

	return frames, err
}

func (c *checkpointer) stop() error {
	c.cancel()
	<-c.done

	return c.conn.Close()
}
