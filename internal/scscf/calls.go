package scscf

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/proxy"
	"github.com/ellanetworks/ims/sip/reginfo"
)

type call struct {
	icid string
}

type bindingKey int64

// TS 24.229 clause 5.4.1.2.2
// TS 24.229 clause 5.4.1.4
// TS 24.229 clause 5.4.1.5
// TS 24.229 clause 5.4.1.8
type calls struct {
	mu        sync.Mutex
	byBinding map[bindingKey]map[*proxy.Dialog]proxy.Side
	byDialog  map[*proxy.Dialog][]bindingKey
	served    map[callKey][]string
}

type callKey struct {
	d *proxy.Dialog
	k bindingKey
}

func newCalls() *calls {
	return &calls{
		byBinding: make(map[bindingKey]map[*proxy.Dialog]proxy.Side),
		byDialog:  make(map[*proxy.Dialog][]bindingKey),
		served:    make(map[callKey][]string),
	}
}

func (c *calls) add(d *proxy.Dialog, k bindingKey, side proxy.Side, served []string) {
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
		c.served[callKey{d, k}] = served
	}
}

func (c *calls) forget(d *proxy.Dialog) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, k := range c.byDialog[d] {
		delete(c.byBinding[k], d)
		delete(c.served, callKey{d, k})

		if len(c.byBinding[k]) == 0 {
			delete(c.byBinding, k)
		}
	}

	delete(c.byDialog, d)
}

func (r *Registrar) dialogEvent(_ context.Context, e proxy.DialogEvent) {
	if e.Kind != proxy.EventEnded {
		return
	}

	if c, ok := e.Dialog.Value().(*call); ok && e.End == proxy.EndReleased {
		r.cfg.Records.Released(c.icid)
	}

	r.calls.forget(e.Dialog)
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

func (c *calls) serving(k bindingKey, pick func(served []string) bool) map[*proxy.Dialog]proxy.Side {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make(map[*proxy.Dialog]proxy.Side)

	for d, side := range c.byBinding[k] {
		if pick(c.served[callKey{d, k}]) {
			out[d] = side
		}
	}

	return out
}

// TS 24.229 clause 5.4.5.1.2
// TS 24.229 clause 5.4.5.1.2A
func (r *Registrar) releaseCalls(ctx context.Context, st *state, removed []removal) {
	cause, err := sip.NewReason(sip.ReasonSIP, 480, sip.ReasonText(sip.ReasonSIP, 480))
	if err != nil {
		return
	}

	rel := proxy.Release{Toward: proxy.Both, Reason: []sip.Reason{cause}, Code: 480, ResponseReason: []sip.Reason{cause}}

	for _, rm := range removed {
		for _, b := range rm.bindings {
			if rm.event == reginfo.Expired && st.bound(b.Contact) {
				continue
			}

			k := bindingKey(b.ID)

			calls := rm.calls[k]
			if rm.calls == nil {
				calls = r.calls.of(k)
			}

			for d, side := range calls {
				var err error

				if side == proxy.Caller {
					err = d.Release(ctx, rel)
				} else {
					err = d.ReleaseCallee(ctx, k, rel)
				}

				switch {
				case err == nil:
					r.log.InfoContext(ctx, "call released on deregistration", slog.String("impi", rm.reg.IMPI),
						slog.String("call-id", d.CallID()), slog.String("contact", b.Contact.URI))
				case errors.Is(err, proxy.ErrDialogEnded), errors.Is(err, proxy.ErrNotReached):
				default:
					r.log.WarnContext(ctx, "failed to release a call", slog.String("call-id", d.CallID()), slog.Any("error", err))
				}
			}
		}
	}
}
