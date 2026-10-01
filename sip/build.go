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

	cseq, err := invite.Header.CSeq()
	if err != nil {
		return nil, err
	}

	via, err := invite.Header.first("Via")
	if err != nil {
		return nil, err
	}

	top, _ := firstListElement(via)

	c := &Request{Method: "CANCEL", URI: invite.URI.Clone(), Version: Version, Envelope: Envelope{Flow: invite.Flow}}
	c.Header.Add("Via", top)

	for _, f := range invite.Header {
		if nameOf("Route").matches(f.Name) {
			c.Header = append(c.Header, f)
		}
	}

	c.Header.Add("Max-Forwards", "70")

	for _, name := range []string{"From", "To", "Call-ID"} {
		v, err := invite.Header.first(name)
		if err != nil {
			return nil, err
		}

		c.Header.Add(name, v)
	}

	c.Header.Add("CSeq", CSeq{Seq: cseq.Seq, Method: "CANCEL"}.String())
	c.Header.Add("Content-Length", "0")

	return c, nil
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
