//go:build linux && (amd64 || arm64)

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/core/diameter/cx"
	"github.com/ellanetworks/ims/internal/hsstest"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/reginfo"
	"github.com/ellanetworks/ims/sip/ua"
	"github.com/ellanetworks/ims/testue"
)

// TS 24.229 §5.7.1.1 a to e, §5.4.2.1.1, §5.4.2.1.2; TS 23.003 §13.4, §13.4B: of an implicit registration set of a
// tel URI and a barred temporary IMPU alone, as Ella Core's HSS gives, the third-party REGISTER is to the tel URI, and
// the AS subscribes to the reg event package with the SIP URI of its number, "user=phone", at the home domain, as the
// MCPTT AS does (registrar.subscribed): the S-CSCF resolves it to the registration of the tel URI, whose documents
// report it active, then terminated once the UE deregisters.
func TestASRegEventTelURI(t *testing.T) {
	s := newISCScene(t)
	srv := newAS(t, 5092)
	sub := subscriberAt(0)

	s.hss.Update(sub.impi, func(h *hsstest.Subscriber) {
		h.IMPUs = []cx.ProfileIdentity{{Identity: tempIMPU, Barred: true}, {Identity: sub.tel}}
	})
	s.setIFCs(0, ifc(0, srv.URI(), cx.DefaultHandlingSessionContinued, methodIs("REGISTER")))

	u := s.newUE(false, testue.Config{Transport: sip.UDP})
	s.register(u)

	r := srv.next(t)

	to, _ := r.Header.To()
	if r.Method != "REGISTER" || to.URI.String() != sub.tel {
		t.Fatalf("AS got:\n%s\nwant a REGISTER to the tel URI", r)
	}

	scscf, err := sip.ParseAddress(r.Header.Get("Contact"))
	if err != nil {
		t.Fatal(err)
	}

	number, err := sip.ParseURI("sip:" + strings.TrimPrefix(sub.tel, "tel:") + "@" + domain + ";user=phone")
	if err != nil {
		t.Fatal(err)
	}

	as := sip.Address{URI: r.URI}
	route := sip.URI{Scheme: "sip", Host: scscf.URI.Host, Port: scscf.URI.Port}
	route.Params.Set("lr", "")

	req := sip.NewRequest("SUBSCRIBE", number)
	req.Header.Set("From", as.String())
	req.Header.Set("To", sip.Address{URI: number}.String())
	req.Header.Set("Event", "reg")
	req.Header.Set("Accept", reginfo.ContentType)
	req.Header.Set("P-Asserted-Identity", as.String())
	req.Header.Set("Route", sip.Address{URI: route}.String())

	accepted := make(chan *sip.Response, 1)
	docs := make(chan reginfo.Reginfo, 8)

	_, err = srv.agent.Subscribe(req, ua.SubscriptionHandler{
		Accepted: func(_ *ua.Subscription, res *sip.Response) { accepted <- res },
		Notify: func(_ *ua.Subscription, n *ua.Request) {
			part, ok, err := n.Message().Content().Find(reginfo.ContentType)
			if err != nil || !ok {
				return
			}

			if d, err := reginfo.Decode(part.Body); err == nil {
				docs <- d
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-accepted:
	case <-time.After(10 * time.Second):
		t.Fatal("the SUBSCRIBE of the SIP URI of the number not accepted")
	}

	// state is the state of the registration of the tel URI of the next document.
	state := func() reginfo.RegState {
		t.Helper()

		select {
		case d := <-docs:
			for _, reg := range d.Registrations {
				if reg.AOR == sub.tel {
					return reg.State
				}
			}

			t.Fatalf("document %+v, want a registration of %s", d, sub.tel)
		case <-time.After(10 * time.Second):
			t.Fatal("no NOTIFY")
		}

		return ""
	}

	if st := state(); st != reginfo.Active {
		t.Fatalf("registration %s, want active", st)
	}

	if err := u.Deregister(s.ctx()); err != nil {
		t.Fatal(err)
	}

	if st := state(); st != reginfo.Terminated {
		t.Fatalf("registration %s after the deregistration, want terminated", st)
	}
}
