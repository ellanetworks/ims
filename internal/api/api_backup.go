package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ellanetworks/ims/internal/cluster"
)

type Backups interface {
	Backup(ctx context.Context, w io.Writer) error
	Restore(ctx context.Context, r io.Reader) error
}

func Backup(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.Backups == nil {
			writeError(r.Context(), w, http.StatusServiceUnavailable, "Backups are not available", nil, cfg.Logger)
			return
		}

		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

		p, _ := PrincipalFrom(r.Context())
		name := fmt.Sprintf("ims-backup-%s.tar.gz", cfg.Now().UTC().Format("20060102T150405Z"))

		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)

		if err := cfg.Backups.Backup(r.Context(), w); err != nil {
			cfg.Logger.ErrorContext(r.Context(), "failed to back up the database", "error", err)
			panic(http.ErrAbortHandler)
		}

		a.audit(r, "backup", p.Email, "Database backed up")
	})
}

func Restore(a *auth) http.Handler {
	cfg := a.cfg

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.Backups == nil {
			writeError(r.Context(), w, http.StatusServiceUnavailable, "Backups are not available", nil, cfg.Logger)
			return
		}

		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Time{})
		_ = rc.SetWriteDeadline(time.Time{})

		err := cfg.Backups.Restore(r.Context(), r.Body)
		if notLeader(w, r, cfg, err) {
			return
		}

		switch {
		case errors.Is(err, cluster.ErrInvalidBackup):
			writeError(r.Context(), w, http.StatusBadRequest, err.Error(), err, cfg.Logger)
			return
		case errors.Is(err, cluster.ErrNotLeader):
			writeError(r.Context(), w, http.StatusServiceUnavailable, "Not ready to restore, retry later", err, cfg.Logger)
			return
		case err != nil:
			writeError(r.Context(), w, http.StatusInternalServerError, "Failed to restore the backup", err, cfg.Logger)
			return
		}

		p, _ := PrincipalFrom(r.Context())
		a.audit(r, "restore", p.Email, "Database restored from a backup")
		writeResponse(r.Context(), w, SuccessResponse{Message: "Backup restored successfully"}, http.StatusOK, cfg.Logger)
	})
}
