package dialog_test

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
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
	d.PrepareResponse(req, ok)

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

	if got := bye.Header.Elements("Route"); strings.Join(got, ", ") != "<sip:proxy2>, <sip:proxy3;lr>, <sip:proxy4>, <sip:user@remoteua>" {
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

func TestRouteSetRecomputedInOrder(t *testing.T) {
	req := parse[*sip.Request](t, invite)

	early := response(t, 183, "10 INVITE", "bt", "Contact: <sip:bob@10.0.0.2>\r\n")
	early.Header.Del("Record-Route")

	d, err := dialog.NewUAC(req, early)
	if err != nil {
		t.Fatal(err)
	}

	if err := d.ReceiveResponse(response(t, 200, "10 INVITE", "bt", "Contact: <sip:bob@10.0.0.2>\r\n")); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(d.RouteSet(), "|"); got != "<sip:p1.example;lr;ftag=x>;foo|<sip:p2.example;lr>" {
		t.Errorf("route set %s", got)
	}
}

func TestProvisionalAfterConfirmed(t *testing.T) {
	d := newUAC(t)

	if err := d.ReceiveResponse(response(t, 183, "10 INVITE", "bt", "Contact: <sip:bob@10.9.9.9>\r\n")); err != nil {
		t.Fatal(err)
	}

	if d.State() != dialog.Confirmed || d.RemoteTarget().String() != "sip:bob@10.0.0.2:5062;transport=tcp" {
		t.Errorf("a late 183 changed the dialog: %v, %s", d.State(), d.RemoteTarget())
	}
}

func TestTargetRefreshRules(t *testing.T) {
	d := newUAC(t)
	initial := d.RemoteTarget().String()

	reinvite, err := d.NewRequest("INVITE")
	if err != nil {
		t.Fatal(err)
	}

	cseq, _ := reinvite.Header.CSeq()

	if err := d.ReceiveResponse(response(t, 180, cseq.String(), "bt", "Contact: <sip:bob@10.0.0.7>\r\n")); err != nil {
		t.Fatal(err)
	}

	if err := d.ReceiveResponse(response(t, 200, cseq.String(), "bt", "")); err != nil {
		t.Fatalf("2xx to a target refresh without Contact: %v", err)
	}

	if got := d.RemoteTarget().String(); got != initial {
		t.Errorf("target %s, want %s unchanged", got, initial)
	}

	info, _ := d.NewRequest("INFO")
	icseq, _ := info.Header.CSeq()

	if err := d.ReceiveResponse(response(t, 200, icseq.String(), "bt", "Contact: <sip:bob@10.0.0.8>\r\n")); err != nil || d.RemoteTarget().String() != initial {
		t.Errorf("2xx to INFO refreshed the target to %s (%v)", d.RemoteTarget(), err)
	}
}

func TestReceivedTargetRefreshRules(t *testing.T) {
	req := parse[*sip.Request](t, invite)
	res := sip.NewResponse(req, 200, "")
	_ = res.Header.SetToTag("bt")

	d, err := dialog.NewUAS(req, res)
	if err != nil {
		t.Fatal(err)
	}

	for i, c := range []struct {
		method, contact, want string
	}{
		{"INVITE", "", "sip:alice@10.0.0.1:5060"},
		{"NOTIFY", "<sip:alice@10.0.0.3>", "sip:alice@10.0.0.3"},
		{"SUBSCRIBE", "<sip:alice@10.0.0.4>", "sip:alice@10.0.0.4"},
		{"MESSAGE", "<sip:alice@10.0.0.5>", "sip:alice@10.0.0.4"},
	} {
		extra := ""
		if c.contact != "" {
			extra = "Contact: " + c.contact + "\r\n"
		}

		if err := d.ReceiveRequest(inDialog(t, c.method, 11+i, extra)); err != nil {
			t.Fatalf("%s: %v", c.method, err)
		}

		if got := d.RemoteTarget().String(); got != c.want {
			t.Errorf("after %s: target %s, want %s", c.method, got, c.want)
		}
	}

	if err := d.ReceiveRequest(inDialog(t, "INFO", 14, "")); err != nil {
		t.Errorf("equal CSeq rejected: %v", err)
	}
}

func TestPrepareResponseOtherRequests(t *testing.T) {
	req := parse[*sip.Request](t, invite)
	res := sip.NewResponse(req, 180, "")
	_ = res.Header.SetToTag("bt")

	d, err := dialog.NewUAS(req, res)
	if err != nil {
		t.Fatal(err)
	}

	prack := inDialog(t, "PRACK", 11, "Record-Route: <sip:other;lr>\r\n")
	ok := sip.NewResponse(prack, 200, "")
	d.PrepareResponse(prack, ok)

	if ok.Header.Has("Record-Route") || d.State() != dialog.Early {
		t.Errorf("200 to PRACK: Record-Route %q, state %v", ok.Header.Values("Record-Route"), d.State())
	}
}

func TestCopyRecordRouteKeepsExisting(t *testing.T) {
	req := parse[*sip.Request](t, invite)
	res := sip.NewResponse(req, 200, "")
	res.Header.Add("Record-Route", "<sip:mine;lr>")

	dialog.CopyRecordRoute(res, req)

	if got := res.Header.Values("Record-Route"); len(got) != 1 || got[0] != "<sip:mine;lr>" {
		t.Errorf("Record-Route %q", got)
	}
}

func TestAckCopiesCredentials(t *testing.T) {
	d := newUAC(t)

	req := parse[*sip.Request](t, invite)
	req.Header.Insert("Proxy-Authorization", `Digest username="alice", realm="p"`)

	ack, err := d.NewAck(req)
	if err != nil {
		t.Fatal(err)
	}

	if ack.Header.Get("Authorization") == "" || ack.Header.Get("Proxy-Authorization") == "" {
		t.Errorf("ACK:\n%s", ack)
	}
}

func TestEarlyDialogTermination(t *testing.T) {
	req := parse[*sip.Request](t, invite)

	d, err := dialog.NewUAC(req, response(t, 183, "10 INVITE", "bt", "Contact: <sip:bob@10.0.0.2>\r\n"))
	if err != nil {
		t.Fatal(err)
	}

	if err := d.ReceiveResponse(response(t, 487, "10 INVITE", "proxy-tag", "")); err != nil {
		t.Fatal(err)
	}

	if d.State() != dialog.Terminated {
		t.Errorf("state %v after 487, want Terminated", d.State())
	}

	if _, err := d.NewRequest("UPDATE"); !errors.Is(err, dialog.ErrTerminated) {
		t.Errorf("NewRequest on a terminated dialog: %v", err)
	}

	res := sip.NewResponse(req, 180, "")
	_ = res.Header.SetToTag("bt")

	uas, err := dialog.NewUAS(req, res)
	if err != nil {
		t.Fatal(err)
	}

	busy := sip.NewResponse(req, 486, "")
	_ = busy.Header.SetToTag("bt")
	uas.PrepareResponse(req, busy)

	if uas.State() != dialog.Terminated || busy.Header.Has("Record-Route") {
		t.Errorf("UAS after 486: %v, Record-Route %q", uas.State(), busy.Header.Values("Record-Route"))
	}
}

func TestByeTerminates(t *testing.T) {
	d := newUAC(t)

	bye, err := d.NewRequest("BYE")
	if err != nil {
		t.Fatal(err)
	}

	cseq, _ := bye.Header.CSeq()

	if err := d.ReceiveResponse(response(t, 200, cseq.String(), "bt", "")); err != nil {
		t.Fatal(err)
	}

	if d.State() != dialog.Terminated {
		t.Errorf("UAC state %v after 200 to BYE", d.State())
	}

	req := parse[*sip.Request](t, invite)
	res := sip.NewResponse(req, 200, "")
	_ = res.Header.SetToTag("bt")

	uas, err := dialog.NewUAS(req, res)
	if err != nil {
		t.Fatal(err)
	}

	if err := uas.ReceiveRequest(inDialog(t, "BYE", 11, "")); err != nil {
		t.Fatal(err)
	}

	if uas.State() != dialog.Terminated {
		t.Errorf("UAS state %v after BYE", uas.State())
	}

	err = uas.ReceiveRequest(inDialog(t, "INFO", 12, ""))

	var serr *sip.StatusError
	if !errors.As(err, &serr) || serr.StatusCode != 481 {
		t.Errorf("request after BYE: %v, want 481", err)
	}
}

const subscribe = "SUBSCRIBE sip:alice@ims.example SIP/2.0\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.9;branch=z9hG4bKs\r\n" +
	"From: <sip:pcscf@ims.example>;tag=ps\r\n" +
	"To: <sip:alice@ims.example>\r\n" +
	"Call-ID: sub-1\r\n" +
	"CSeq: 5 SUBSCRIBE\r\n" +
	"Event: reg\r\n" +
	"Contact: <sip:pcscf@10.0.0.9>\r\n" +
	"Content-Length: 0\r\n\r\n"

func notify(t *testing.T, toTag, state string) *sip.Request {
	t.Helper()

	return parse[*sip.Request](t, "NOTIFY sip:pcscf@10.0.0.9 SIP/2.0\r\n"+
		"Via: SIP/2.0/UDP s.example;branch=z9hG4bKn\r\n"+
		"Record-Route: <sip:i.example;lr>, <sip:s2.example;lr>\r\n"+
		"From: <sip:alice@ims.example>;tag=notifier\r\n"+
		"To: <sip:pcscf@ims.example>;tag="+toTag+"\r\n"+
		"Call-ID: sub-1\r\n"+
		"CSeq: 1 NOTIFY\r\n"+
		"Event: reg\r\n"+
		"Subscription-State: "+state+"\r\n"+
		"Contact: <sip:scscf@10.0.0.8:6060>\r\n"+
		"Content-Length: 0\r\n\r\n")
}

func TestNewFromNotify(t *testing.T) {
	sub := parse[*sip.Request](t, subscribe)

	d, err := dialog.NewFromNotify(sub, notify(t, "ps", "active;expires=600"))
	if err != nil {
		t.Fatal(err)
	}

	if got, want := d.ID(), (dialog.ID{CallID: "sub-1", LocalTag: "ps", RemoteTag: "notifier"}); got != want {
		t.Errorf("ID %+v, want %+v", got, want)
	}

	if got := strings.Join(d.RouteSet(), "|"); got != "<sip:i.example;lr>|<sip:s2.example;lr>" {
		t.Errorf("route set %s", got)
	}

	if d.State() != dialog.Confirmed || d.RemoteTarget().String() != "sip:scscf@10.0.0.8:6060" || d.NextHop().String() != "sip:i.example;lr" {
		t.Errorf("state %v, target %s, next hop %s", d.State(), d.RemoteTarget(), d.NextHop())
	}

	if seq, _ := d.RemoteSeq(); seq != 1 {
		t.Errorf("remote CSeq %d", seq)
	}

	refresh, err := d.NewRequest("SUBSCRIBE")
	if err != nil {
		t.Fatal(err)
	}

	if cseq, _ := refresh.Header.CSeq(); cseq.Seq != 6 {
		t.Errorf("refresh CSeq %d, want 6", cseq.Seq)
	}

	if _, err := dialog.NewFromNotify(sub, notify(t, "other", "active")); !errors.Is(err, dialog.ErrMismatch) {
		t.Errorf("NOTIFY for another subscription: %v", err)
	}

	if _, err := dialog.NewFromNotify(sub, notify(t, "ps", "terminated;reason=noresource")); !errors.Is(err, dialog.ErrNoDialog) {
		t.Errorf("terminated NOTIFY: %v", err)
	}
}

func TestNextHop(t *testing.T) {
	d := newUAC(t)

	if got := d.NextHop().String(); got != "sip:p1.example;lr;ftag=x" {
		t.Errorf("loose next hop %s", got)
	}

	req := parse[*sip.Request](t, invite)
	res := response(t, 200, "10 INVITE", "bt", "Contact: <sip:user@10.0.0.2>\r\n")
	res.Header.Del("Record-Route")

	direct, err := dialog.NewUAC(req, res)
	if err != nil {
		t.Fatal(err)
	}

	if got := direct.NextHop().String(); got != "sip:user@10.0.0.2" {
		t.Errorf("next hop without a route set %s", got)
	}

	res.Header.Set("Record-Route", "<sip:10.0.0.3;lr>, <sip:10.0.0.4>")

	strict, err := dialog.NewUAC(req, res)
	if err != nil {
		t.Fatal(err)
	}

	bye, _ := strict.NewRequest("BYE")

	if got := strict.NextHop().String(); got != "sip:10.0.0.4" || bye.URI.String() != got {
		t.Errorf("strict next hop %s, Request-URI %s", got, bye.URI)
	}

	if _, addr, err := sip.Destination(strict.NextHop()); err != nil || addr.String() != "10.0.0.4:5060" {
		t.Errorf("destination %s, %v", addr, err)
	}
}

func TestStrictRouteStripsMethodAndHeaders(t *testing.T) {
	req := parse[*sip.Request](t, invite)
	res := response(t, 200, "10 INVITE", "bt", "Contact: <sip:user@10.0.0.2>\r\n")
	res.Header.Set("Record-Route", "<sip:10.0.0.4;method=INVITE;maddr=10.0.0.5?x=y>")

	d, err := dialog.NewUAC(req, res)
	if err != nil {
		t.Fatal(err)
	}

	bye, _ := d.NewRequest("BYE")
	if got := bye.URI.String(); got != "sip:10.0.0.4;maddr=10.0.0.5" {
		t.Errorf("Request-URI %s", got)
	}
}

func roundTrip(t *testing.T, d *dialog.Dialog) *dialog.Dialog {
	t.Helper()

	b, err := json.Marshal(d.Snapshot())
	if err != nil {
		t.Fatal(err)
	}

	var s dialog.Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}

	r, err := dialog.Restore(s)
	if err != nil {
		t.Fatalf("Restore: %v\n%s", err, b)
	}

	return r
}

func TestSnapshotRoundTrip(t *testing.T) {
	req := parse[*sip.Request](t, invite)
	res := sip.NewResponse(req, 180, "")
	_ = res.Header.SetToTag("bt")

	uas, err := dialog.NewUAS(req, res)
	if err != nil {
		t.Fatal(err)
	}

	notifier, err := dialog.NewFromNotify(parse[*sip.Request](t, subscribe), notify(t, "ps", "active"))
	if err != nil {
		t.Fatal(err)
	}

	for name, d := range map[string]*dialog.Dialog{"UAS": uas, "UAC": newUAC(t), "NOTIFY": notifier} {
		r := roundTrip(t, d)

		if r.ID() != d.ID() || r.State() != d.State() {
			t.Errorf("%s: ID %+v, state %v, want %+v, %v", name, r.ID(), r.State(), d.ID(), d.State())
		}

		if r.RemoteTarget().String() != d.RemoteTarget().String() || !slices.Equal(r.RouteSet(), d.RouteSet()) {
			t.Errorf("%s: target %s, route set %v, want %s, %v", name, r.RemoteTarget(), r.RouteSet(), d.RemoteTarget(), d.RouteSet())
		}

		ls, lok := r.LocalSeq()
		rs, rok := r.RemoteSeq()
		wantLS, wantLOK := d.LocalSeq()
		wantRS, wantROK := d.RemoteSeq()

		if ls != wantLS || lok != wantLOK || rs != wantRS || rok != wantROK {
			t.Errorf("%s: CSeqs %d/%v %d/%v, want %d/%v %d/%v", name, ls, lok, rs, rok, wantLS, wantLOK, wantRS, wantROK)
		}

		if r.Snapshot().Origin != d.Snapshot().Origin {
			t.Errorf("%s: origin %v, want %v", name, r.Snapshot().Origin, d.Snapshot().Origin)
		}
	}

	ok := sip.NewResponse(req, 200, "")
	_ = ok.Header.SetToTag("bt")

	restored := roundTrip(t, uas)
	restored.PrepareResponse(req, ok)

	if restored.State() != dialog.Confirmed {
		t.Errorf("200 to the INVITE after restore: state %v", restored.State())
	}
}

func TestRestoredLocalSeq(t *testing.T) {
	d := roundTrip(t, newUAC(t))

	n, err := d.NewRequest("NOTIFY")
	if err != nil {
		t.Fatal(err)
	}

	if cseq, _ := n.Header.CSeq(); cseq.Seq != 11 {
		t.Errorf("NOTIFY CSeq %d, want 11", cseq.Seq)
	}

	if got := n.Header.Get("To"); got != "<sip:bob@b.example>;tag=bt" {
		t.Errorf("To = %s", got)
	}

	req := parse[*sip.Request](t, invite)
	res := sip.NewResponse(req, 200, "")
	_ = res.Header.SetToTag("bt")

	uas, err := dialog.NewUAS(req, res)
	if err != nil {
		t.Fatal(err)
	}

	r := roundTrip(t, uas)
	if _, ok := r.LocalSeq(); ok {
		t.Fatal("restored UAS has a local CSeq")
	}

	bye, err := r.NewRequest("BYE")
	if err != nil {
		t.Fatal(err)
	}

	if cseq, _ := bye.Header.CSeq(); cseq.Seq == 0 || cseq.Seq >= 1<<31 {
		t.Errorf("first local CSeq %d not in [1, 2^31)", cseq.Seq)
	}
}

func TestRestoredRemoteSeq(t *testing.T) {
	req := parse[*sip.Request](t, invite)
	res := sip.NewResponse(req, 200, "")
	_ = res.Header.SetToTag("bt")

	uas, err := dialog.NewUAS(req, res)
	if err != nil {
		t.Fatal(err)
	}

	if err := uas.ReceiveRequest(inDialog(t, "INFO", 20, "")); err != nil {
		t.Fatal(err)
	}

	d := roundTrip(t, uas)

	err = d.ReceiveRequest(inDialog(t, "INFO", 19, ""))

	var serr *sip.StatusError
	if !errors.As(err, &serr) || serr.StatusCode != 500 {
		t.Errorf("lower CSeq after restore: err = %v, want 500", err)
	}

	if err := d.ReceiveRequest(inDialog(t, "INFO", 21, "")); err != nil {
		t.Errorf("higher CSeq after restore: %v", err)
	}
}

func TestRestoreErrors(t *testing.T) {
	good := newUAC(t).Snapshot()

	for name, mutate := range map[string]func(*dialog.Snapshot){
		"bad target":    func(s *dialog.Snapshot) { s.Target = "not a uri" },
		"bad local":     func(s *dialog.Snapshot) { s.Local = "<sip:" },
		"bad route":     func(s *dialog.Snapshot) { s.Route = []string{"<sip:"} },
		"no remote tag": func(s *dialog.Snapshot) { s.Remote = "<sip:bob@b.example>" },
		"ID mismatch":   func(s *dialog.Snapshot) { s.ID.LocalTag = "zz" },
		"invalid state": func(s *dialog.Snapshot) { s.State = 7 },
		"no local tag":  func(s *dialog.Snapshot) { s.Local = "<sip:alice@a.example>" },
	} {
		s := good
		s.Route = slices.Clone(good.Route)
		mutate(&s)

		if _, err := dialog.Restore(s); err == nil {
			t.Errorf("%s: no error", name)
		}
	}

	s := good
	s.Remote = "<sip:bob@b.example>"

	if _, err := dialog.Restore(s); !errors.Is(err, dialog.ErrNoDialog) {
		t.Errorf("missing tag: err = %v, want ErrNoDialog", err)
	}
}
