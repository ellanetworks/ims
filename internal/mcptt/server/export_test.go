package server

import (
	"github.com/ellanetworks/ims/internal/mcptt/controlling"
	"github.com/ellanetworks/ims/internal/mcptt/participating"
	"github.com/ellanetworks/ims/sip"
)

type terminating struct {
	p *participating.Participating
	f func(req *sip.Request, t participating.Termination) participating.Termination
}

func (w terminating) Terminate(req *sip.Request) participating.Termination {
	return w.f(req, w.p.Terminate(req))
}

func InterposeTerminate(s *Server, f func(req *sip.Request, t participating.Termination) participating.Termination) {
	s.ctrlCalls = controlling.New(controlling.Config{
		Agent: s.agent, Source: s.cfg.Source, Affiliations: s.controlling, Media: s.cfg.Media,
		Participating: terminating{s.partCalls, f},
	})
}
