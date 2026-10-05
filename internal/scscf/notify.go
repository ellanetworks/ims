package scscf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ellanetworks/ims/internal/db"
	"github.com/ellanetworks/ims/internal/regevent"
	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
)

const (
	reasonNoResource  = "noresource"
	reasonRejected    = "rejected"
	reasonDeactivated = "deactivated"
	reasonTimeout     = "timeout"
)

type removal struct {
	reg      db.Registration
	bindings []db.Binding
	event    regevent.Event
	byUE     bool
}

type change struct {
	removed   []removal
	reason    string
	shortened string
}

type outgoing struct {
	impi  string
	subID int64
	req   *sip.Request
	final bool
}

func (r *Registrar) notifyChange(ctx context.Context, impi string, ch change) []*outgoing {
	r.notifySharing(ctx, impi, ch)

	return r.notifySubscriptions(ctx, impi, ch)
}

func (r *Registrar) notifySubscriptions(ctx context.Context, impi string, ch change) []*outgoing {
	subs, err := r.cfg.DB.ListRegSubscriptions(ctx, impi)
	if err != nil {
		r.log.Warn("failed to list the reg event subscriptions", slog.String("impi", impi), slog.Any("error", err))
		return nil
	}

	if len(subs) == 0 {
		return nil
	}

	st, err := r.load(ctx, impi)
	if err != nil {
		r.log.Warn("failed to read the registrations", slog.String("impi", impi), slog.Any("error", err))
		return nil
	}

	info, err := r.reginfo(ctx, st, ch)
	if err != nil {
		r.log.Warn("failed to build the reg event state", slog.String("impi", impi), slog.Any("error", err))
		return nil
	}

	reason := ch.reason
	if reason == "" {
		reason = reasonNoResource
	}

	active := slices.ContainsFunc(info.Registrations, func(reg regevent.Registration) bool {
		return reg.State == regevent.Active
	})

	var out []*outgoing

	for _, s := range subs {
		state := "terminated;reason=" + reason

		switch gone, byUE := ueGone(s, st, ch); {
		case gone && byUE:
			r.dropSubscription(ctx, s)
			continue
		case !active:
		case gone:
			state = "terminated;reason=" + reasonNoResource
		case !s.ExpiresAt.After(st.now):
			continue
		default:
			state = activeState(s.ExpiresAt, st.now)
		}

		if o := r.buildNotify(ctx, st, s, &info, state); o != nil {
			out = append(out, o)
		}
	}

	return out
}

func (r *Registrar) notifySharing(ctx context.Context, impi string, ch change) {
	var sets []db.Registration

	for _, rm := range ch.removed {
		sets = append(sets, rm.reg)
	}

	if st, err := r.load(ctx, impi); err == nil {
		sets = append(sets, st.regs...)
	}

	others := map[string]bool{}

	for _, set := range sets {
		for _, id := range set.Identities {
			regs, err := r.cfg.DB.ListRegistrationsByIdentity(ctx, id.Key)
			if err != nil {
				continue
			}

			for _, reg := range regs {
				if reg.IMPI != impi {
					others[reg.IMPI] = true
				}
			}
		}
	}

	for other := range others {
		r.background(other, func(ctx context.Context) {
			r.send(r.notifySubscriptions(ctx, other, ch))
		})
	}
}

func ueGone(s db.RegSubscription, st *state, ch change) (gone, byUE bool) {
	if s.Subscriber != db.SubscriberUE {
		return false, false
	}

	target, err := sip.ParseURI(s.RemoteTarget)
	if err != nil {
		return false, false
	}

	removed := false

	for _, rm := range ch.removed {
		for _, b := range rm.bindings {
			if sameUA(b.Contact.URI, target) {
				removed = true
				byUE = byUE || rm.byUE
			}
		}
	}

	if !removed {
		return false, false
	}

	for _, reg := range st.regs {
		for _, b := range st.live(reg.Bindings) {
			if sameUA(b.Contact.URI, target) {
				return false, false
			}
		}
	}

	return true, byUE
}

func sameUA(contact string, target sip.URI) bool {
	u, err := sip.ParseURI(contact)

	return err == nil && strings.EqualFold(u.Host, target.Host) && u.Port == target.Port
}

func activeState(expires, now time.Time) string {
	return "active;expires=" + strconv.FormatInt(seconds(expires.Sub(now)), 10)
}

func seconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}

	return int64((d + time.Second - 1) / time.Second)
}

func (r *Registrar) dropSubscription(ctx context.Context, s db.RegSubscription) {
	if err := r.cfg.DB.DeleteRegSubscription(ctx, s.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
		r.log.Warn("failed to delete a reg event subscription", slog.String("impi", s.IMPI), slog.Any("error", err))
	}
}

func (r *Registrar) buildNotify(ctx context.Context, st *state, s db.RegSubscription, info *regevent.Reginfo,
	subState string,
) *outgoing {
	final := strings.HasPrefix(subState, "terminated")

	o, err := r.newNotify(ctx, st, &s, info, subState)
	if err != nil {
		r.log.Warn("failed to build a NOTIFY", slog.String("impi", s.IMPI), slog.Any("error", err))
		r.dropSubscription(ctx, s)

		return nil
	}

	if final {
		r.dropSubscription(ctx, s)
	} else if err := r.cfg.DB.UpdateRegSubscription(ctx, s); err != nil {
		r.log.Warn("failed to store a reg event subscription", slog.String("impi", s.IMPI), slog.Any("error", err))
		return nil
	}

	o.final = final

	return o
}

func (r *Registrar) newNotify(ctx context.Context, st *state, s *db.RegSubscription, info *regevent.Reginfo,
	subState string,
) (*outgoing, error) {
	d, err := restoreDialog(s.Dialog)
	if err != nil {
		return nil, err
	}

	req, err := d.NewRequest("NOTIFY")
	if err != nil {
		return nil, err
	}

	if info == nil {
		current, err := r.reginfo(ctx, st, change{})
		if err != nil {
			return nil, err
		}

		info = &current
	}

	s.Version++

	doc := *info
	doc.Version = uint64(max(s.Version, 0))

	body, err := regevent.Encode(doc)
	if err != nil {
		return nil, err
	}

	if s.Dialog, err = json.Marshal(d.Snapshot()); err != nil {
		return nil, err
	}

	tr, dest, err := sip.NextHop(req)
	if err != nil {
		return nil, err
	}

	local, ok := r.listener(dest.Addr())
	if !ok {
		return nil, fmt.Errorf("no S-CSCF listener for %s", dest.Addr())
	}

	req.Header.Prepend("Via", sip.NewVia(tr, local).String())
	req.Header.Add("Contact", contactURI(local, tr))
	req.Header.Add("Event", "reg")
	req.Header.Add("Subscription-State", subState)
	req.SetBody(regevent.ContentType, body)
	req.Flow = sip.Flow{Transport: tr, Local: local, Remote: dest}

	return &outgoing{impi: s.IMPI, subID: s.ID, req: req}, nil
}

func restoreDialog(b []byte) (*dialog.Dialog, error) {
	var snap dialog.Snapshot

	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("stored dialog: %w", err)
	}

	return dialog.Restore(snap)
}

func (r *Registrar) listener(dest netip.Addr) (netip.AddrPort, bool) {
	dest = dest.Unmap()

	for _, l := range r.cfg.Listeners {
		if l.Addr().Unmap() == dest {
			return l, true
		}
	}

	for _, l := range r.cfg.Listeners {
		if l.Addr().Unmap().Is4() == dest.Is4() {
			return l, true
		}
	}

	return netip.AddrPort{}, false
}

func contactURI(local netip.AddrPort, tr sip.Transport) string {
	u := sip.URI{Scheme: "sip", Host: sip.FormatHost(local.Addr().Unmap()), Port: local.Port()}
	if tr != sip.UDP {
		u.Params.Set("transport", strings.ToLower(string(tr)))
	}

	return "<" + u.String() + ">"
}

func (r *Registrar) reginfo(ctx context.Context, st *state, ch change) (regevent.Reginfo, error) {
	info := regevent.Reginfo{State: regevent.Full}

	var sets []db.Registration

	for _, reg := range st.regs {
		if st.registered(&reg) {
			sets = append(sets, reg)
		}
	}

	for _, rm := range ch.removed {
		if rm.reg.IMPI == st.impi {
			sets = append(sets, rm.reg)
		}
	}

	seen := map[string]bool{}

	for _, set := range sets {
		for _, id := range set.Identities {
			if id.Barred || seen[id.Key] {
				continue
			}

			seen[id.Key] = true

			reg, err := r.registrationElement(ctx, st, ch, id)
			if err != nil {
				return regevent.Reginfo{}, err
			}

			info.Registrations = append(info.Registrations, reg)
		}
	}

	return info, nil
}

func (r *Registrar) registrationElement(ctx context.Context, st *state, ch change, id db.PublicIdentity) (regevent.Registration, error) {
	reg := regevent.Registration{AOR: id.URI, ID: elementID("r", id.Key), State: regevent.Terminated}

	bound, err := r.cfg.DB.ListRegistrationsByIdentity(ctx, id.Key)
	if err != nil {
		return regevent.Registration{}, err
	}

	var active []string

	for _, set := range bound {
		for _, b := range liveAt(set.Bindings, st.now) {
			event := regevent.Refreshed

			switch {
			case ch.shortened != "" && set.IMPI == ch.shortened:
				event = regevent.Shortened
			case b.Event != db.BindingRefreshed:
				event = regevent.Created

				registering := b.IMPU
				if registering == "" {
					registering = identityKeyOf(set.IMPU)
				}

				if registering == id.Key {
					event = regevent.Registered
				}
			}

			reg.Contacts = append(reg.Contacts, contactElement(b, id.Key, regevent.Active, event, st.now))
			reg.State = regevent.Active

			active = append(active, b.Contact.URI)
		}
	}

	for _, rm := range ch.removed {
		if !holds(rm.reg.Identities, id.Key) {
			continue
		}

		for _, b := range rm.bindings {
			if slices.Contains(active, b.Contact.URI) {
				continue
			}

			reg.Contacts = append(reg.Contacts, contactElement(b, id.Key, regevent.Terminated, rm.event, st.now))
		}
	}

	return reg, nil
}

func contactElement(b db.Binding, key, state string, event regevent.Event, now time.Time) regevent.Contact {
	expires := uint32(0)
	if state == regevent.Active {
		expires = uint32(min(seconds(b.ExpiresAt.Sub(now)), 1<<32-1))
	}

	cseq := uint32(b.CSeq)

	c := regevent.Contact{
		ID: elementID("c", strconv.FormatInt(b.Contact.ID, 10)+"|"+key+"|"+
			strconv.FormatInt(b.RegisteredAt.UnixNano(), 10)),
		State:   state,
		Event:   event,
		Expires: &expires,
		CallID:  b.CallID,
		CSeq:    &cseq,
		URI:     b.Contact.URI,
	}

	params, err := sip.ParseParams(b.Contact.Params)
	if err != nil {
		return c
	}

	for _, p := range params {
		switch strings.ToLower(p.Name) {
		case "expires":
		case "q":
			c.Q = p.Value
		default:
			c.UnknownParams = append(c.UnknownParams, regevent.UnknownParam{Name: p.Name, Value: p.Value})
		}
	}

	return c
}

func elementID(prefix, key string) string {
	sum := sha256.Sum256([]byte(key))

	return prefix + hex.EncodeToString(sum[:8])
}

func (r *Registrar) send(out []*outgoing) {
	for _, o := range out {
		if _, err := r.cfg.Layer.Request(o.req, &notifyTransaction{r: r, o: o}); err != nil {
			r.log.Info("sending a NOTIFY failed", slog.String("impi", o.impi), slog.Any("error", err))
			r.notifyFailed(o)
		}
	}
}

type notifyTransaction struct {
	r *Registrar
	o *outgoing
}

func (t *notifyTransaction) HandleResponse(res *sip.Response) {
	switch {
	case res.StatusCode < 200:
	case res.IsSuccess():
		t.r.notified(t.o, res)
	case endsSubscription(res.StatusCode):
		t.r.log.Info("NOTIFY refused", slog.String("impi", t.o.impi), slog.Int("code", res.StatusCode))
		t.r.notifyFailed(t.o)
	// RFC 3261 §12.2.1.2: a 408 within a dialog SHOULD end it, as a timeout does
	// under RFC 6665 §4.2.2. RFC 4320 forbids it for a NOTIFY, but not every proxy complies.
	case res.StatusCode == 408:
		t.r.log.Info("NOTIFY timed out", slog.String("impi", t.o.impi))
		t.r.notifyFailed(t.o)
	default:
		t.r.log.Debug("NOTIFY failed", slog.String("impi", t.o.impi), slog.Int("code", res.StatusCode))
	}
}

func (t *notifyTransaction) HandleError(err error) {
	t.r.log.Info("NOTIFY failed", slog.String("impi", t.o.impi), slog.Any("error", err))
	t.r.notifyFailed(t.o)
}

func endsSubscription(code int) bool {
	switch {
	case code == 404, code == 405, code == 410, code == 416, code >= 480 && code <= 485, code == 489, code == 501,
		code == 604:
		return true
	}

	return false
}

func (r *Registrar) notifyFailed(o *outgoing) {
	if o.final {
		return
	}

	r.background(o.impi, func(ctx context.Context) {
		s, err := r.cfg.DB.GetRegSubscriptionByID(ctx, o.subID)
		if err != nil {
			return
		}

		r.dropSubscription(ctx, s)
		r.log.Info("reg event subscription ended by a failed NOTIFY", slog.String("impi", s.IMPI), slog.String("impu", s.IMPU))
	})
}

func (r *Registrar) notified(o *outgoing, res *sip.Response) {
	if o.final || !res.Header.Has("Contact") {
		return
	}

	r.background(o.impi, func(ctx context.Context) {
		s, err := r.cfg.DB.GetRegSubscriptionByID(ctx, o.subID)
		if err != nil {
			return
		}

		d, err := restoreDialog(s.Dialog)
		if err != nil || d.ReceiveResponse(res) != nil {
			return
		}

		if s.Dialog, err = json.Marshal(d.Snapshot()); err != nil {
			return
		}

		s.RemoteTarget = d.RemoteTarget().String()

		if err := r.cfg.DB.UpdateRegSubscription(ctx, s); err != nil && !errors.Is(err, db.ErrNotFound) {
			r.log.Warn("failed to store a reg event subscription", slog.String("impi", s.IMPI), slog.Any("error", err))
		}
	})
}

func (r *Registrar) background(impi string, f func(ctx context.Context)) {
	if !r.start() {
		return
	}

	go func() {
		defer r.wg.Done()

		if err := r.lock(r.ctx, impi); err != nil {
			return
		}

		defer r.unlock(impi)

		f(r.ctx)
	}()
}
