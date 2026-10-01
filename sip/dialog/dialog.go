package dialog

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"

	"github.com/ellanetworks/ims/sip"
)

type State int

const (
	Early State = iota
	Confirmed
	Terminated
)

func (s State) String() string {
	switch s {
	case Early:
		return "Early"
	case Confirmed:
		return "Confirmed"
	case Terminated:
		return "Terminated"
	}

	return "State(?)"
}

var (
	ErrNoDialog = errors.New("sip/dialog: message does not create a dialog")

	ErrMismatch = errors.New("sip/dialog: message belongs to another dialog")

	ErrCSeqExhausted = errors.New("sip/dialog: local CSeq exhausted")

	ErrTerminated = errors.New("sip/dialog: dialog terminated")
)

type ID struct {
	CallID    string
	LocalTag  string
	RemoteTag string
}

func RequestID(req *sip.Request) ID {
	return ID{CallID: req.Header.CallID(), LocalTag: tag(req.Header.To()), RemoteTag: tag(req.Header.From())}
}

func ResponseID(res *sip.Response) ID {
	return ID{CallID: res.Header.CallID(), LocalTag: tag(res.Header.From()), RemoteTag: tag(res.Header.To())}
}

func tag(a sip.Address, err error) string {
	if err != nil {
		return ""
	}

	return a.Tag()
}

type Dialog struct {
	mu sync.Mutex

	id     ID
	state  State
	local  sip.Address
	remote sip.Address
	target sip.URI
	route  []string

	localSeq     uint32
	haveLocalSeq bool

	remoteSeq     uint32
	haveRemoteSeq bool

	origin sip.CSeq
}

func NewUAC(req *sip.Request, res *sip.Response) (*Dialog, error) {
	if err := creates(res); err != nil {
		return nil, err
	}

	from, to, cseq, err := endpoints(req, res)
	if err != nil {
		return nil, err
	}

	target, err := contact(res.Header)
	if err != nil {
		return nil, err
	}

	route := res.Header.Elements("Record-Route")
	slices.Reverse(route)

	d, err := newDialog(req.Header.CallID(), from, to, target, route, cseq)
	if err != nil {
		return nil, err
	}

	d.localSeq, d.haveLocalSeq = cseq.Seq, true

	if res.IsSuccess() {
		d.state = Confirmed
	}

	return d, nil
}

func NewUAS(req *sip.Request, res *sip.Response) (*Dialog, error) {
	if err := creates(res); err != nil {
		return nil, err
	}

	from, to, cseq, err := endpoints(req, res)
	if err != nil {
		return nil, err
	}

	target, err := contact(req.Header)
	if err != nil {
		return nil, err
	}

	d, err := newDialog(req.Header.CallID(), to, from, target, req.Header.Elements("Record-Route"), cseq)
	if err != nil {
		return nil, err
	}

	d.remoteSeq, d.haveRemoteSeq = cseq.Seq, true
	d.PrepareResponse(req, res)

	return d, nil
}

func NewFromNotify(subscribe, notify *sip.Request) (*Dialog, error) {
	if notify.Method != "NOTIFY" {
		return nil, fmt.Errorf("%w: %s request", ErrNoDialog, notify.Method)
	}

	if state, _, _ := strings.Cut(notify.Header.Get("Subscription-State"), ";"); strings.EqualFold(strings.TrimSpace(state), "terminated") {
		return nil, fmt.Errorf("%w: subscription terminated", ErrNoDialog)
	}

	from, err := subscribe.Header.From()
	if err != nil {
		return nil, err
	}

	notifier, err := notify.Header.From()
	if err != nil {
		return nil, err
	}

	if id := RequestID(notify); id.CallID != subscribe.Header.CallID() || id.LocalTag != from.Tag() {
		return nil, ErrMismatch
	}

	cseq, err := subscribe.Header.CSeq()
	if err != nil {
		return nil, err
	}

	remote, err := notify.Header.CSeq()
	if err != nil {
		return nil, err
	}

	target, err := contact(notify.Header)
	if err != nil {
		return nil, err
	}

	d, err := newDialog(subscribe.Header.CallID(), from, notifier, target, notify.Header.Elements("Record-Route"), cseq)
	if err != nil {
		return nil, err
	}

	d.state = Confirmed
	d.localSeq, d.haveLocalSeq = cseq.Seq, true
	d.remoteSeq, d.haveRemoteSeq = remote.Seq, true

	return d, nil
}

func creates(res *sip.Response) error {
	if res.StatusCode <= 100 || res.StatusCode >= 300 {
		return fmt.Errorf("%w: %d response", ErrNoDialog, res.StatusCode)
	}

	return nil
}

func endpoints(req *sip.Request, res *sip.Response) (from, to sip.Address, cseq sip.CSeq, err error) {
	if from, err = req.Header.From(); err != nil {
		return
	}

	if to, err = res.Header.To(); err != nil {
		return
	}

	cseq, err = req.Header.CSeq()

	return
}

func newDialog(callID string, local, remote sip.Address, target sip.URI, route []string, origin sip.CSeq) (*Dialog, error) {
	if local.Tag() == "" || remote.Tag() == "" {
		return nil, fmt.Errorf("%w: missing tag", ErrNoDialog)
	}

	if err := checkRoute(route); err != nil {
		return nil, err
	}

	return &Dialog{
		id:     ID{CallID: callID, LocalTag: local.Tag(), RemoteTag: remote.Tag()},
		local:  local,
		remote: remote,
		target: target,
		route:  route,
		origin: origin,
	}, nil
}

func checkRoute(route []string) error {
	for _, r := range route {
		if _, err := sip.ParseAddress(r); err != nil {
			return fmt.Errorf("sip/dialog: route set: %w", err)
		}
	}

	return nil
}

func contact(h sip.Header) (sip.URI, error) {
	u, ok, err := optionalContact(h)
	if err == nil && !ok {
		err = fmt.Errorf("%w: no Contact", ErrNoDialog)
	}

	return u, err
}

func optionalContact(h sip.Header) (sip.URI, bool, error) {
	cs, err := h.Contacts()
	if err != nil {
		return sip.URI{}, false, err
	}

	if len(cs) == 0 {
		return sip.URI{}, false, nil
	}

	if cs[0].Star {
		return sip.URI{}, false, errors.New("sip/dialog: Contact * in a dialog")
	}

	return cs[0].URI, true, nil
}

func (d *Dialog) ID() ID {
	return d.id
}

func (d *Dialog) State() State {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.state
}

func (d *Dialog) Terminate() {
	d.mu.Lock()
	d.state = Terminated
	d.mu.Unlock()
}

func (d *Dialog) RemoteTarget() sip.URI {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.target.Clone()
}

func (d *Dialog) RouteSet() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return slices.Clone(d.route)
}

func (d *Dialog) NextHop() sip.URI {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.route) == 0 {
		return d.target.Clone()
	}

	first, _ := sip.ParseAddress(d.route[0])

	return first.URI
}

func (d *Dialog) LocalSeq() (uint32, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.localSeq, d.haveLocalSeq
}

func (d *Dialog) RemoteSeq() (uint32, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.remoteSeq, d.haveRemoteSeq
}

func CopyRecordRoute(res *sip.Response, req *sip.Request) {
	if res.Header.Has("Record-Route") {
		return
	}

	var rr sip.Header

	for _, f := range req.Header {
		if strings.EqualFold(f.Name, "Record-Route") {
			rr = append(rr, f)
		}
	}

	if len(rr) == 0 {
		return
	}

	i := 0

	for j, f := range res.Header {
		if strings.EqualFold(sip.LongName(f.Name), "Via") {
			i = j + 1
		}
	}

	res.Header = slices.Insert(res.Header, i, rr...)
}

func (d *Dialog) PrepareResponse(req *sip.Request, res *sip.Response) {
	cseq, err := req.Header.CSeq()
	if err != nil || cseq != d.origin || res.StatusCode <= 100 {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	switch {
	case res.StatusCode >= 300:
		if d.state == Early {
			d.state = Terminated
		}
	case d.state == Terminated:
	default:
		CopyRecordRoute(res, req)

		if res.IsSuccess() {
			d.state = Confirmed
		}
	}
}

func isTargetRefresh(method string) bool {
	switch method {
	case "INVITE", "UPDATE", "SUBSCRIBE", "NOTIFY":
		return true
	}

	return false
}

func (d *Dialog) ReceiveRequest(req *sip.Request) error {
	if id := RequestID(req); id != d.id {
		return ErrMismatch
	}

	if req.Method == "ACK" || req.Method == "CANCEL" {
		return nil
	}

	cseq, err := req.Header.CSeq()
	if err != nil {
		return err
	}

	var (
		target  sip.URI
		refresh bool
	)

	if isTargetRefresh(req.Method) {
		if target, refresh, err = optionalContact(req.Header); err != nil {
			return &sip.StatusError{StatusCode: 400, Err: err}
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.state == Terminated {
		return &sip.StatusError{StatusCode: 481, Err: ErrTerminated}
	}

	if d.haveRemoteSeq && cseq.Seq < d.remoteSeq {
		return &sip.StatusError{StatusCode: 500, Err: fmt.Errorf("CSeq %d below %d", cseq.Seq, d.remoteSeq)}
	}

	d.remoteSeq, d.haveRemoteSeq = cseq.Seq, true

	if refresh {
		d.target = target
	}

	if req.Method == "BYE" {
		d.state = Terminated
	}

	return nil
}

func (d *Dialog) ReceiveResponse(res *sip.Response) error {
	cseq, err := res.Header.CSeq()
	if err != nil {
		return err
	}

	creating := cseq == d.origin
	id := ResponseID(res)

	if id.CallID != d.id.CallID || id.LocalTag != d.id.LocalTag || (id.RemoteTag != d.id.RemoteTag && (!creating || res.StatusCode < 300)) {
		return ErrMismatch
	}

	if res.StatusCode <= 100 {
		return nil
	}

	target, hasTarget, err := optionalContact(res.Header)
	if err != nil {
		return err
	}

	route := res.Header.Elements("Record-Route")
	slices.Reverse(route)

	if err := checkRoute(route); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	switch {
	case d.state == Terminated:
	case creating && res.StatusCode >= 300:
		if d.state == Early {
			d.state = Terminated
		}
	case cseq.Method == "BYE" && res.StatusCode >= 200:
		d.state = Terminated
	case creating && d.state == Early:
		if hasTarget {
			d.target = target
		}

		if res.IsSuccess() {
			d.state = Confirmed
			d.route = route
		}
	case res.IsSuccess() && isTargetRefresh(cseq.Method) && hasTarget:
		d.target = target
	}

	return nil
}

func (d *Dialog) NewRequest(method string) (*sip.Request, error) {
	if method == "ACK" || method == "CANCEL" {
		return nil, fmt.Errorf("sip/dialog: %s is not built by NewRequest", method)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	switch {
	case d.state == Terminated:
		return nil, ErrTerminated
	case !d.haveLocalSeq:
		d.localSeq, d.haveLocalSeq = initialSeq(), true
	case d.localSeq == math.MaxUint32:
		return nil, ErrCSeqExhausted
	default:
		d.localSeq++
	}

	return d.build(method, d.localSeq), nil
}

func (d *Dialog) NewAck(invite *sip.Request) (*sip.Request, error) {
	cseq, err := invite.Header.CSeq()
	if err != nil {
		return nil, err
	}

	if invite.Method != "INVITE" {
		return nil, fmt.Errorf("sip/dialog: ACK for a %s request", invite.Method)
	}

	d.mu.Lock()
	ack := d.build("ACK", cseq.Seq)
	d.mu.Unlock()

	for _, f := range invite.Header {
		if strings.EqualFold(f.Name, "Authorization") || strings.EqualFold(f.Name, "Proxy-Authorization") {
			ack.Header.Insert(f.Name, f.Value)
		}
	}

	return ack, nil
}

func (d *Dialog) NewPrack(res *sip.Response) (*sip.Request, error) {
	rseq, err := res.Header.RSeq()
	if err != nil {
		return nil, err
	}

	cseq, err := res.Header.CSeq()
	if err != nil {
		return nil, err
	}

	prack, err := d.NewRequest("PRACK")
	if err != nil {
		return nil, err
	}

	prack.Header.Insert("RAck", sip.RAck{RSeq: rseq, CSeq: cseq.Seq, Method: cseq.Method}.String())

	return prack, nil
}

func (d *Dialog) build(method string, seq uint32) *sip.Request {
	uri := d.target
	route := d.route

	if len(route) > 0 {
		first, _ := sip.ParseAddress(route[0])

		if !first.URI.IsLooseRouter() {
			uri = first.URI
			uri.Headers = ""
			uri.Params.Del("method")

			route = append(slices.Clone(route[1:]), "<"+d.target.String()+">")
		}
	}

	req := sip.NewRequest(method, uri)
	req.Header = nil

	if len(route) > 0 {
		req.Header.Add("Route", strings.Join(route, ", "))
	}

	req.Header.Add("Max-Forwards", "70")
	req.Header.Add("From", d.local.String())
	req.Header.Add("To", d.remote.String())
	req.Header.Add("Call-ID", d.id.CallID)
	req.Header.Add("CSeq", sip.CSeq{Seq: seq, Method: method}.String())
	req.Header.Add("Content-Length", "0")

	return req
}

func initialSeq() uint32 {
	var b [4]byte

	_, _ = rand.Read(b[:])

	return binary.BigEndian.Uint32(b[:])%(1<<31-1) + 1
}
