package sip

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
)

func NewResponse(req *Request, code int, reason string) *Response {
	if code < 100 || code > 699 {
		panic(fmt.Sprintf("sip: invalid status code %d", code))
	}

	if reason == "" {
		reason = ReasonPhrase(code)
	}

	res := &Response{Version: Version, StatusCode: code, Reason: reason, Envelope: Envelope{Flow: req.Flow}}

	copied := []headerName{nameOf("Via"), nameOf("From"), nameOf("To"), nameOf("Call-ID"), nameOf("CSeq")}
	if code == 100 {
		copied = append(copied, nameOf("Timestamp"))
	}

	for _, f := range req.Header {
		for _, h := range copied {
			if h.matches(f.Name) {
				res.Header = append(res.Header, f)
				break
			}
		}
	}

	res.Header.Add("Content-Length", "0")

	return res
}

func NewCancel(invite *Request) (*Request, error) {
	if invite.Method != "INVITE" {
		return nil, errors.New("sip: CANCEL of a non-INVITE request")
	}

	to, err := invite.Header.first("To")
	if err != nil {
		return nil, err
	}

	return newInviteRequest("CANCEL", invite, to)
}

func NewAck(invite *Request, res *Response) (*Request, error) {
	if invite.Method != "INVITE" {
		return nil, errors.New("sip: ACK of a non-INVITE request")
	}

	if res.StatusCode < 300 {
		return nil, fmt.Errorf("sip: ACK to a %d response is built by the dialog", res.StatusCode)
	}

	to, err := res.Header.first("To")
	if err != nil {
		return nil, err
	}

	return newInviteRequest("ACK", invite, to)
}

func newInviteRequest(method string, invite *Request, to string) (*Request, error) {
	cseq, err := invite.Header.CSeq()
	if err != nil {
		return nil, err
	}

	via, err := invite.Header.first("Via")
	if err != nil {
		return nil, err
	}

	top, _ := firstListElement(via)

	r := &Request{Method: method, URI: invite.URI.Clone(), Version: Version, Envelope: Envelope{Flow: invite.Flow}}
	r.Header.Add("Via", top)

	for _, f := range invite.Header {
		if nameOf("Route").matches(f.Name) {
			r.Header = append(r.Header, f)
		}
	}

	r.Header.Add("Max-Forwards", "70")

	from, err := invite.Header.first("From")
	if err != nil {
		return nil, err
	}

	r.Header.Add("From", from)
	r.Header.Add("To", to)

	callID, err := invite.Header.first("Call-ID")
	if err != nil {
		return nil, err
	}

	r.Header.Add("Call-ID", callID)
	r.Header.Add("CSeq", CSeq{Seq: cseq.Seq, Method: method}.String())
	r.Header.Add("Content-Length", "0")

	return r, nil
}

func NewTag() string {
	return rand.Text()
}

func NewBranch() string {
	return MagicCookie + rand.Text()
}

var reasonPhrases = map[int]string{
	100: "Trying",
	180: "Ringing",
	181: "Call Is Being Forwarded",
	182: "Queued",
	183: "Session Progress",
	199: "Early Dialog Terminated",
	200: "OK",
	202: "Accepted",
	204: "No Notification",
	300: "Multiple Choices",
	301: "Moved Permanently",
	302: "Moved Temporarily",
	305: "Use Proxy",
	380: "Alternative Service",
	400: "Bad Request",
	401: "Unauthorized",
	402: "Payment Required",
	403: "Forbidden",
	404: "Not Found",
	405: "Method Not Allowed",
	406: "Not Acceptable",
	407: "Proxy Authentication Required",
	408: "Request Timeout",
	410: "Gone",
	412: "Conditional Request Failed",
	413: "Request Entity Too Large",
	414: "Request-URI Too Long",
	415: "Unsupported Media Type",
	416: "Unsupported URI Scheme",
	417: "Unknown Resource-Priority",
	420: "Bad Extension",
	421: "Extension Required",
	422: "Session Interval Too Small",
	423: "Interval Too Brief",
	430: "Flow Failed",
	433: "Anonymity Disallowed",
	439: "First Hop Lacks Outbound Support",
	469: "Bad Info Package",
	480: "Temporarily Unavailable",
	481: "Call/Transaction Does Not Exist",
	482: "Loop Detected",
	483: "Too Many Hops",
	484: "Address Incomplete",
	485: "Ambiguous",
	486: "Busy Here",
	487: "Request Terminated",
	488: "Not Acceptable Here",
	489: "Bad Event",
	491: "Request Pending",
	493: "Undecipherable",
	494: "Security Agreement Required",
	500: "Server Internal Error",
	501: "Not Implemented",
	502: "Bad Gateway",
	503: "Service Unavailable",
	504: "Server Time-out",
	505: "Version Not Supported",
	513: "Message Too Large",
	580: "Precondition Failure",
	600: "Busy Everywhere",
	603: "Decline",
	604: "Does Not Exist Anywhere",
	606: "Not Acceptable",
}

func ReasonPhrase(code int) string {
	if r, ok := reasonPhrases[code]; ok {
		return r
	}

	return "Status " + strconv.Itoa(code)
}
