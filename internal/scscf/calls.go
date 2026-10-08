package scscf

import (
	"errors"
	"log/slog"
	"sync"

	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
)

// bindingKey identifies a binding: a contact registered for an implicit registration set.
type bindingKey struct {
	reg, contact int64
}

// calls indexes the INVITE dialogs of the S-CSCF by the bindings they were initiated by (the
// caller's) or terminated toward (the callee's), so that removing a binding releases them
// (TS 24.229 §5.4.1.2.2 step 4A, §5.4.1.4 step 5, §5.4.1.5).
type calls struct {
	mu        sync.Mutex
	byBinding map[bindingKey]map[*proxy.Dialog]proxy.Side
	byDialog  map[*proxy.Dialog][]bindingKey
}

func newCalls() *calls {
	return &calls{
		byBinding: make(map[bindingKey]map[*proxy.Dialog]proxy.Side),
		byDialog:  make(map[*proxy.Dialog][]bindingKey),
	}
}

func (c *calls) add(d *proxy.Dialog, k bindingKey, side proxy.Side) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ds := c.byBinding[k]
	if ds == nil {
		ds = make(map[*proxy.Dialog]proxy.Side)
		c.byBinding[k] = ds
	}

	if _, ok := ds[d]; !ok {
		ds[d] = side
		c.byDialog[d] = append(c.byDialog[d], k)
	}
}

func (c *calls) forget(d *proxy.Dialog) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, k := range c.byDialog[d] {
		delete(c.byBinding[k], d)

		if len(c.byBinding[k]) == 0 {
			delete(c.byBinding, k)
		}
	}

	delete(c.byDialog, d)
}

func (c *calls) event(e proxy.DialogEvent) {
	if e.Kind == proxy.EventEnded {
		c.forget(e.Dialog)
	}
}

func (c *calls) of(k bindingKey) map[*proxy.Dialog]proxy.Side {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make(map[*proxy.Dialog]proxy.Side, len(c.byBinding[k]))
	for d, side := range c.byBinding[k] {
		out[d] = side
	}

	return out
}

// releaseCalls releases the INVITE dialogs of the bindings removed, other than by expiry, with
// a 480 in their Reason (TS 24.229 §5.4.5.1.2): a dialog the contact initiated toward both
// parties, one terminated toward it on its own branches only, so that the other contacts of a
// forked call ring on.
func (r *Registrar) releaseCalls(removed []removal) {
	cause, err := sip.NewReason(sip.ReasonSIP, 480, sip.ReasonText(sip.ReasonSIP, 480))
	if err != nil {
		return
	}

	rel := proxy.Release{Toward: proxy.Both, Reason: []sip.Reason{cause}, Code: 480, ResponseReason: []sip.Reason{cause}}

	for _, rm := range removed {
		if rm.event == regevent.Expired {
			continue
		}

		for _, b := range rm.bindings {
			k := bindingKey{reg: rm.reg.ID, contact: b.Contact.ID}

			for d, side := range r.calls.of(k) {
				var err error

				if side == proxy.Caller {
					err = d.Release(rel)
				} else {
					err = d.ReleaseCallee(k, rel)
				}

				switch {
				case err == nil:
					r.log.Info("call released on deregistration", slog.String("impi", rm.reg.IMPI),
						slog.String("call-id", d.CallID()), slog.String("contact", b.Contact.URI))
				case errors.Is(err, proxy.ErrDialogEnded), errors.Is(err, proxy.ErrNotReached):
				default:
					r.log.Warn("failed to release a call", slog.String("call-id", d.CallID()), slog.Any("error", err))
				}
			}
		}
	}
}
