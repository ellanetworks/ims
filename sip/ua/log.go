package ua

import (
	"log/slog"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
)

// The log records of a dialog carry its Call-ID and tags (RFC 3261 §12), those of a request outside one its Call-ID,
// so that the records of one call can be found together.

func dialogAttr(id dialog.ID) slog.Attr {
	return slog.Group("dialog",
		slog.String("call_id", id.CallID), slog.String("local_tag", id.LocalTag), slog.String("remote_tag", id.RemoteTag))
}

func callAttr(req *sip.Request) slog.Attr {
	return slog.String("call_id", req.Header.CallID())
}

func (s *Session) logger() *slog.Logger {
	return s.a.log.With(dialogAttr(s.id))
}

func (s *ServerSubscription) logger() *slog.Logger {
	return s.a.log.With(dialogAttr(s.d.ID()))
}

func (s *Subscription) logger() *slog.Logger {
	if s.d == nil {
		return s.a.log
	}

	return s.a.log.With(dialogAttr(s.d.ID()))
}

func (r *Request) logger() *slog.Logger {
	if r.s != nil {
		return r.s.logger()
	}

	return r.a.log.With(callAttr(r.req))
}
