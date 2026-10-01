package dialog_test

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/dialog"
)

const invite = "INVITE sip:bob@b.example SIP/2.0\r\n" +
	"Via: SIP/2.0/UDP p2.example;branch=z9hG4bKp2\r\n" +
	"Via: SIP/2.0/UDP p1.example;branch=z9hG4bKp1\r\n" +
	"Via: SIP/2.0/UDP a.example;branch=z9hG4bKa\r\n" +
	"Record-Route: <sip:p2.example;lr>\r\n" +
	"Record-Route: <sip:p1.example;lr;ftag=x>;foo\r\n" +
	"Max-Forwards: 68\r\n" +
	"From: \"Alice\" <sip:alice@a.example>;tag=at\r\n" +
	"To: <sip:bob@b.example>\r\n" +
	"Call-ID: call-1\r\n" +
	"CSeq: 10 INVITE\r\n" +
	"Contact: <sip:alice@10.0.0.1:5060>\r\n" +
	"Authorization: Digest username=\"alice\"\r\n" +
	"Content-Length: 0\r\n\r\n"

func parse[T sip.Message](t *testing.T, raw string) T {
	t.Helper()

	m, err := sip.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	v, ok := m.(T)
	if !ok {
		t.Fatalf("parsed a %T", m)
	}

	return v
}

func response(t *testing.T, code int, cseq, toTag, extra string) *sip.Response {
	t.Helper()

	to := "<sip:bob@b.example>"
	if toTag != "" {
		to += ";tag=" + toTag
	}

	return parse[*sip.Response](t, "SIP/2.0 "+strconv.Itoa(code)+" "+sip.ReasonPhrase(code)+"\r\n"+
		"Via: SIP/2.0/UDP a.example;branch=z9hG4bKa\r\n"+
		"Record-Route: <sip:p2.example;lr>, <sip:p1.example;lr;ftag=x>;foo\r\n"+
		"From: \"Alice\" <sip:alice@a.example>;tag=at\r\n"+
		"To: "+to+"\r\n"+
		"Call-ID: call-1\r\n"+
		"CSeq: "+cseq+"\r\n"+
		extra+
		"Content-Length: 0\r\n\r\n")
}

func newUAC(t *testing.T) *dialog.Dialog {
	t.Helper()

	req := parse[*sip.Request](t, invite)

	d, err := dialog.NewUAC(req, response(t, 200, "10 INVITE", "bt", "Contact: <sip:bob@10.0.0.2:5062;transport=tcp>\r\n"))
	if err != nil {
		t.Fatal(err)
	}

	return d
}

func TestUACFromFinal(t *testing.T) {
	d := newUAC(t)

	if got, want := d.ID(), (dialog.ID{CallID: "call-1", LocalTag: "at", RemoteTag: "bt"}); got != want {
		t.Errorf("ID = %+v, want %+v", got, want)
	}

	if d.State() != dialog.Confirmed {
		t.Errorf("State = %v, want Confirmed", d.State())
	}

	if got := strings.Join(d.RouteSet(), ", "); got != "<sip:p1.example;lr;ftag=x>;foo, <sip:p2.example;lr>" {
		t.Errorf("route set = %s", got)
	}

	if _, ok := d.RemoteSeq(); ok {
		t.Error("remote CSeq set on the UAC side")
	}

	bye, err := d.NewRequest("BYE")
	if err != nil {
		t.Fatal(err)
	}

	want := "BYE sip:bob@10.0.0.2:5062;transport=tcp SIP/2.0\r\n" +
		"Route: <sip:p1.example;lr;ftag=x>;foo, <sip:p2.example;lr>\r\n" +
		"Max-Forwards: 70\r\n" +
		"From: \"Alice\" <sip:alice@a.example>;tag=at\r\n" +
		"To: <sip:bob@b.example>;tag=bt\r\n" +
		"Call-ID: call-1\r\n" +
		"CSeq: 11 BYE\r\n" +
		"Content-Length: 0\r\n\r\n"
	if got := bye.String(); got != want {
		t.Errorf("BYE:\n%s\nwant:\n%s", got, want)
	}
}

func TestUACEarlyThenConfirmed(t *testing.T) {
	req := parse[*sip.Request](t, invite)

	early := response(t, 183, "10 INVITE", "bt", "Contact: <sip:bob@10.0.0.2>\r\nRSeq: 7\r\n")
	early.Header.Del("Record-Route")

	d, err := dialog.NewUAC(req, early)
	if err != nil {
		t.Fatal(err)
	}

	if d.State() != dialog.Early || len(d.RouteSet()) != 0 {
		t.Fatalf("early dialog: state %v, route set %v", d.State(), d.RouteSet())
	}

	prack, err := d.NewPrack(early)
	if err != nil {
		t.Fatal(err)
	}

	if got := prack.Header.Get("RAck"); got != "7 10 INVITE" {
		t.Errorf("RAck = %q", got)
	}

	if cseq, _ := prack.Header.CSeq(); cseq.Seq != 11 {
		t.Errorf("PRACK CSeq = %d, want 11", cseq.Seq)
	}

	if err := d.ReceiveResponse(response(t, 200, "10 INVITE", "bt", "Contact: <sip:bob@10.0.0.3>\r\n")); err != nil {
		t.Fatal(err)
	}

	if d.State() != dialog.Confirmed {
		t.Errorf("State = %v, want Confirmed", d.State())
	}

	if got := len(d.RouteSet()); got != 2 {
		t.Errorf("route set not recomputed from the 2xx: %v", d.RouteSet())
	}

	if got := d.RemoteTarget().String(); got != "sip:bob@10.0.0.3" {
		t.Errorf("remote target = %s", got)
	}

	ack, err := d.NewAck(req)
	if err != nil {
		t.Fatal(err)
	}

	if cseq, _ := ack.Header.CSeq(); cseq != (sip.CSeq{Seq: 10, Method: "ACK"}) {
		t.Errorf("ACK CSeq = %v", cseq)
	}

	if ack.Header.Get("Authorization") == "" {
		t.Error("ACK lacks the INVITE's Authorization")
	}

	if seq, _ := d.LocalSeq(); seq != 11 {
		t.Errorf("ACK changed the local CSeq to %d", seq)
	}
}

func TestNoDialog(t *testing.T) {
	req := parse[*sip.Request](t, invite)

	for _, res := range []*sip.Response{
		response(t, 100, "10 INVITE", "", ""),
		response(t, 180, "10 INVITE", "", "Contact: <sip:bob@10.0.0.2>\r\n"),
		response(t, 486, "10 INVITE", "bt", ""),
		response(t, 200, "10 INVITE", "bt", ""),
	} {
		if _, err := dialog.NewUAC(req, res); !errors.Is(err, dialog.ErrNoDialog) {
			t.Errorf("%s: err = %v, want ErrNoDialog", res.StartLine(), err)
		}
	}
}

func TestUACTargetRefresh(t *testing.T) {
	d := newUAC(t)

	reinvite, err := d.NewRequest("INVITE")
	if err != nil {
		t.Fatal(err)
	}

	cseq, _ := reinvite.Header.CSeq()

	if err := d.ReceiveResponse(response(t, 200, cseq.String(), "bt", "Contact: <sip:bob@10.0.0.9>\r\n")); err != nil {
		t.Fatal(err)
	}

	if got := d.RemoteTarget().String(); got != "sip:bob@10.0.0.9" {
		t.Errorf("remote target = %s", got)
	}

	if got := len(d.RouteSet()); got != 2 {
		t.Errorf("route set changed by a re-INVITE: %v", d.RouteSet())
	}

	if err := d.ReceiveResponse(response(t, 200, cseq.String(), "other", "Contact: <sip:x@10.0.0.9>\r\n")); !errors.Is(err, dialog.ErrMismatch) {
		t.Errorf("response from another dialog: err = %v", err)
	}
}

func TestUAS(t *testing.T) {
	req := parse[*sip.Request](t, invite)

	res := sip.NewResponse(req, 200, "")
	_ = res.Header.SetToTag("bt")

	d, err := dialog.NewUAS(req, res)
	if err != nil {
		t.Fatal(err)
	}

	if got := res.Header.Values("Record-Route"); strings.Join(got, "|") != "<sip:p2.example;lr>|<sip:p1.example;lr;ftag=x>;foo" {
		t.Errorf("Record-Route in the response = %q", got)
	}

	if !strings.Contains(res.String(), "Via: SIP/2.0/UDP a.example;branch=z9hG4bKa\r\nRecord-Route: <sip:p2.example;lr>\r\nRecord-Route: <sip:p1.example;lr;ftag=x>;foo\r\nFrom:") {
		t.Errorf("Record-Route not placed after Via:\n%s", res)
	}

	if got, want := d.ID(), (dialog.ID{CallID: "call-1", LocalTag: "bt", RemoteTag: "at"}); got != want {
		t.Errorf("ID = %+v, want %+v", got, want)
	}

	if seq, ok := d.RemoteSeq(); !ok || seq != 10 {
		t.Errorf("remote CSeq = %d, %v", seq, ok)
	}

	bye, err := d.NewRequest("BYE")
	if err != nil {
		t.Fatal(err)
	}

	if bye.URI.String() != "sip:alice@10.0.0.1:5060" {
		t.Errorf("Request-URI = %s", bye.URI)
	}

	if got := bye.Header.Get("Route"); got != "<sip:p2.example;lr>, <sip:p1.example;lr;ftag=x>;foo" {
		t.Errorf("Route = %s", got)
	}

	if got := bye.Header.Get("From"); got != "<sip:bob@b.example>;tag=bt" {
		t.Errorf("From = %s", got)
	}

	if got := bye.Header.Get("To"); got != "\"Alice\" <sip:alice@a.example>;tag=at" {
		t.Errorf("To = %s", got)
	}

	if cseq, _ := bye.Header.CSeq(); cseq.Seq == 0 || cseq.Seq >= 1<<31 {
		t.Errorf("first local CSeq %d not in [1, 2^31)", cseq.Seq)
	}

	if dialog.RequestID(req) != (dialog.ID{CallID: "call-1", RemoteTag: "at"}) {
		t.Errorf("RequestID = %+v", dialog.RequestID(req))
	}
}

func inDialog(t *testing.T, method string, seq int, extra string) *sip.Request {
	t.Helper()

	return parse[*sip.Request](t, method+" sip:bob@10.0.0.2 SIP/2.0\r\n"+
		"Via: SIP/2.0/UDP a.example;branch=z9hG4bK"+method+strconv.Itoa(seq)+"\r\n"+
		"From: <sip:alice@a.example>;tag=at\r\n"+
		"To: <sip:bob@b.example>;tag=bt\r\n"+
		"Call-ID: call-1\r\n"+
		"CSeq: "+strconv.Itoa(seq)+" "+method+"\r\n"+
		extra+
		"Content-Length: 0\r\n\r\n")
}

func TestUASReceiveRequest(t *testing.T) {
	req := parse[*sip.Request](t, invite)
	res := sip.NewResponse(req, 180, "")
	_ = res.Header.SetToTag("bt")

	d, err := dialog.NewUAS(req, res)
	if err != nil {
		t.Fatal(err)
	}

	if err := d.ReceiveRequest(inDialog(t, "INFO", 12, "Contact: <sip:alice@10.9.9.9>\r\n")); err != nil {
		t.Fatalf("a CSeq gap: %v", err)
	}

	if d.RemoteTarget().String() != "sip:alice@10.0.0.1:5060" {
		t.Errorf("INFO refreshed the target to %s", d.RemoteTarget())
	}

	if err := d.ReceiveRequest(inDialog(t, "UPDATE", 13, "Contact: <sip:alice@10.0.0.5>\r\n")); err != nil {
		t.Fatal(err)
	}

	if d.RemoteTarget().String() != "sip:alice@10.0.0.5" {
		t.Errorf("UPDATE did not refresh the target: %s", d.RemoteTarget())
	}

	err = d.ReceiveRequest(inDialog(t, "UPDATE", 11, "Contact: <sip:alice@10.0.0.6>\r\n"))

	var serr *sip.StatusError
	if !errors.As(err, &serr) || serr.StatusCode != 500 {
		t.Fatalf("out-of-order CSeq: err = %v, want 500", err)
	}

	if d.RemoteTarget().String() != "sip:alice@10.0.0.5" {
		t.Error("a rejected request changed the target")
	}

	if err := d.ReceiveRequest(inDialog(t, "ACK", 10, "")); err != nil {
		t.Errorf("ACK: %v", err)
	}

	if d.State() != dialog.Early {
		t.Errorf("State = %v, want Early", d.State())
	}

	ok := sip.NewResponse(req, 200, "")
	_ = ok.Header.SetToTag("bt")
	d.Respond(req, ok)

	if d.State() != dialog.Confirmed || ok.Header.Count("Record-Route") != 2 {
		t.Errorf("200 to the INVITE: state %v, %d Record-Route", d.State(), ok.Header.Count("Record-Route"))
	}

	other := inDialog(t, "BYE", 14, "")
	other.Header.Set("To", "<sip:bob@b.example>;tag=zz")

	if err := d.ReceiveRequest(other); !errors.Is(err, dialog.ErrMismatch) {
		t.Errorf("request for another dialog: err = %v", err)
	}
}

func TestStrictRouteSet(t *testing.T) {
	req := parse[*sip.Request](t, invite)
	res := response(t, 200, "10 INVITE", "bt", "Contact: <sip:user@remoteua>\r\n")
	res.Header.Set("Record-Route", "<sip:proxy4>, <sip:proxy3;lr>, <sip:proxy2>, <sip:proxy1;transport=tcp?x=y>")

	d, err := dialog.NewUAC(req, res)
	if err != nil {
		t.Fatal(err)
	}

	bye, err := d.NewRequest("BYE")
	if err != nil {
		t.Fatal(err)
	}

	if got := bye.URI.String(); got != "sip:proxy1;transport=tcp" {
		t.Errorf("Request-URI = %s, want sip:proxy1;transport=tcp", got)
	}

	if got := bye.Header.Get("Route"); got != "<sip:proxy2>, <sip:proxy3;lr>, <sip:proxy4>, <sip:user@remoteua>" {
		t.Errorf("Route = %s", got)
	}
}

func TestCSeqExhausted(t *testing.T) {
	req := parse[*sip.Request](t, invite)
	req.Header.Set("CSeq", sip.CSeq{Seq: math.MaxUint32 - 1, Method: "INVITE"}.String())

	d, err := dialog.NewUAC(req, response(t, 200, sip.CSeq{Seq: math.MaxUint32 - 1, Method: "INVITE"}.String(), "bt", "Contact: <sip:bob@10.0.0.2>\r\n"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := d.NewRequest("BYE"); err != nil {
		t.Fatalf("CSeq 2^32-1: %v", err)
	}

	if _, err := d.NewRequest("BYE"); !errors.Is(err, dialog.ErrCSeqExhausted) {
		t.Errorf("err = %v, want ErrCSeqExhausted", err)
	}
}
