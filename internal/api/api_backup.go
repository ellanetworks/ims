package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Backups interface {
	Backup(ctx context.Context, w io.Writer) error
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
