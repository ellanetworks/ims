package siptest

import (
	"net/netip"
	"strconv"

	"github.com/ellanetworks/ims/sip"
)

func NewRequest(method, target string, tr sip.Transport, sentBy netip.AddrPort) *sip.Request {
	uri, err := sip.ParseURI(target)
	if err != nil {
		panic(err)
	}

	r := sip.NewRequest(method, uri)
	r.Header = nil

	host := sip.FormatHost(sentBy.Addr())
	via := sip.Via{Protocol: "SIP/2.0", Transport: tr, Host: host, Port: sentBy.Port()}
	via.Params.Set("branch", sip.NewBranch())
	via.Params.Set("rport", "")

	r.Header.Add("Via", via.String())
	r.Header.Add("Max-Forwards", "70")
	r.Header.Add("From", "<sip:ue@"+host+">;tag="+sip.NewTag())
	r.Header.Add("To", "<"+target+">")
	r.Header.Add("Call-ID", sip.NewTag()+"@"+host)
	r.Header.Add("CSeq", "1 "+method)
	r.Header.Add("Contact", "<sip:ue@"+host+":"+strconv.Itoa(int(sentBy.Port()))+">")
	r.Header.Add("Content-Length", "0")

	return r
}
