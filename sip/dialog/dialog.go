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
)

func (s State) String() string {
	switch s {
	case Early:
		return "Early"
	case Confirmed:
		return "Confirmed"
	}

	return "State(?)"
}

var (
	ErrNoDialog = errors.New("sip/dialog: message does not create a dialog")

	ErrMismatch = errors.New("sip/dialog: message belongs to another dialog")

	ErrCSeqExhausted = errors.New("sip/dialog: local CSeq exhausted")
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
	if res.StatusCode <= 100 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: %d response", ErrNoDialog, res.StatusCode)
	}

	from, err := req.Header.From()
	if err != nil {
		return nil, err
	}

	to, err := res.Header.To()
	if err != nil {
		return nil, err
	}

	if to.Tag() == "" {
		return nil, fmt.Errorf("%w: no To tag", ErrNoDialog)
	}

	cseq, err := req.Header.CSeq()
	if err != nil {
		return nil, err
	}

	target, err := contact(res.Header)
	if err != nil {
		return nil, err
	}

	d := &Dialog{
		id:           ID{CallID: req.Header.CallID(), LocalTag: from.Tag(), RemoteTag: to.Tag()},
		local:        from,
		remote:       to,
		target:       target,
		route:        res.Header.Elements("Record-Route"),
		localSeq:     cseq.Seq,
		haveLocalSeq: true,
		origin:       cseq,
	}

	slices.Reverse(d.route)

	if res.IsSuccess() {
		d.state = Confirmed
	}

	return d, nil
}

func NewUAS(req *sip.Request, res *sip.Response) (*Dialog, error) {
	if res.StatusCode <= 100 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: %d response", ErrNoDialog, res.StatusCode)
	}

	from, err := req.Header.From()
	if err != nil {
		return nil, err
	}

	to, err := res.Header.To()
	if err != nil {
		return nil, err
	}

	if to.Tag() == "" {
		return nil, fmt.Errorf("%w: no To tag in the response", ErrNoDialog)
	}

	cseq, err := req.Header.CSeq()
	if err != nil {
		return nil, err
	}

	target, err := contact(req.Header)
	if err != nil {
		return nil, err
	}

	d := &Dialog{
		id:            ID{CallID: req.Header.CallID(), LocalTag: to.Tag(), RemoteTag: from.Tag()},
		local:         to,
		remote:        from,
		target:        target,
		route:         req.Header.Elements("Record-Route"),
		remoteSeq:     cseq.Seq,
		haveRemoteSeq: true,
		origin:        cseq,
	}

	d.Respond(req, res)

	return d, nil
}

func contact(h sip.Header) (sip.URI, error) {
	cs, err := h.Contacts()
	if err != nil {
		return sip.URI{}, err
	}

	if len(cs) == 0 || cs[0].Star {
		return sip.URI{}, fmt.Errorf("%w: no Contact", ErrNoDialog)
	}

	return cs[0].URI, nil
}

func (d *Dialog) ID() ID {
	return d.id
}

func (d *Dialog) State() State {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.state
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

func (d *Dialog) Respond(req *sip.Request, res *sip.Response) {
	cseq, err := req.Header.CSeq()
	if err != nil || cseq != d.origin || res.StatusCode <= 100 || res.StatusCode >= 300 {
		return
	}

	CopyRecordRoute(res, req)

	if res.IsSuccess() {
		d.mu.Lock()
		d.state = Confirmed
		d.mu.Unlock()
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

	var target sip.URI

	refresh := isTargetRefresh(req.Method) && req.Header.Has("Contact")
	if refresh {
		if target, err = contact(req.Header); err != nil {
			return &sip.StatusError{StatusCode: 400, Err: err}
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.haveRemoteSeq && cseq.Seq < d.remoteSeq {
		return &sip.StatusError{StatusCode: 500, Err: fmt.Errorf("CSeq %d below %d", cseq.Seq, d.remoteSeq)}
	}

	d.remoteSeq, d.haveRemoteSeq = cseq.Seq, true

	if refresh {
		d.target = target
	}

	return nil
}

func (d *Dialog) ReceiveResponse(res *sip.Response) error {
	if id := ResponseID(res); id != d.id {
		return ErrMismatch
	}

	cseq, err := res.Header.CSeq()
	if err != nil {
		return err
	}

	creating := cseq == d.origin

	refresh := res.IsSuccess() && isTargetRefresh(cseq.Method)

	if res.StatusCode <= 100 || res.StatusCode >= 300 || (!creating && !refresh) {
		return nil
	}

	target, err := contact(res.Header)
	if err != nil {
		if res.IsProvisional() {
			return nil
		}

		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if creating && d.state == Confirmed && res.IsProvisional() {
		return nil
	}

	d.target = target

	if creating && res.IsSuccess() && d.state == Early {
		d.state = Confirmed
		d.route = res.Header.Elements("Record-Route")
		slices.Reverse(d.route)
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
	case !d.haveLocalSeq:
		d.localSeq, d.haveLocalSeq = initialSeq(), true
	case d.localSeq == math.MaxUint32:
		return nil, ErrCSeqExhausted
	default:
		d.localSeq++
	}

	return d.build(method, d.localSeq)
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
	ack, err := d.build("ACK", cseq.Seq)
	d.mu.Unlock()

	if err != nil {
		return nil, err
	}

	for _, f := range invite.Header {
		if strings.EqualFold(f.Name, "Authorization") || strings.EqualFold(f.Name, "Proxy-Authorization") {
			ack.Header = slices.Insert(ack.Header, len(ack.Header)-1, f)
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

	rack := sip.RAck{RSeq: rseq, CSeq: cseq.Seq, Method: cseq.Method}
	prack.Header = slices.Insert(prack.Header, len(prack.Header)-1, sip.Field{Name: "RAck", Value: rack.String()})

	return prack, nil
}

func (d *Dialog) build(method string, seq uint32) (*sip.Request, error) {
	uri := d.target
	route := d.route

	if len(route) > 0 {
		first, err := sip.ParseAddress(route[0])
		if err != nil {
			return nil, fmt.Errorf("sip/dialog: route set: %w", err)
		}

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

	return req, nil
}

func initialSeq() uint32 {
	var b [4]byte

	_, _ = rand.Read(b[:])

	return binary.BigEndian.Uint32(b[:])%(1<<31-1) + 1
}
