package ua

import (
	"log/slog"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
)

func dialogAttr(id dialog.ID) slog.Attr {
	return slog.Group("dialog",
		slog.String("call_id", id.CallID), slog.String("local_tag", id.LocalTag), slog.String("remote_tag", id.RemoteTag))
}

func callAttr(req *sip.Request) slog.Attr {
	return slog.String("call_id", req.Header.CallID())
}

func (s *Session) logger() *slog.Logger {
	if s.log == nil {
		s.log = s.a.log.With(dialogAttr(s.id))
	}

	return s.log
}

func (s *ServerSubscription) logger() *slog.Logger {
	if s.log == nil {
		s.log = s.a.log.With(dialogAttr(s.d.ID()))
	}

	return s.log
}

func (s *Subscription) logger() *slog.Logger {
	if s.d == nil {
		return s.a.log
	}

	if s.log == nil {
		s.log = s.a.log.With(dialogAttr(s.d.ID()))
	}

	return s.log
}

func (r *Request) logger() *slog.Logger {
	if r.s != nil {
		return r.s.logger()
	}

	return r.a.log.With(callAttr(r.req))
}
