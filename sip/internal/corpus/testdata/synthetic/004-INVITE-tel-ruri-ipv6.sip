INVITE tel:+15551230002;phone-context=ims.mnc001.mcc001.3gppnetwork.org SIP/2.0
Via: SIP/2.0/TCP [2001:db8:10::5]:50001;branch=z9hG4bKinv6tel;rport
Max-Forwards: 70
Route: <sip:[2001:db8::1]:5103;lr>, <sip:orig@scscf.ims.mnc001.mcc001.3gppnetwork.org:6060;lr>
From: "Alice" <tel:+15551230001>;tag=f00d
To: <tel:+15551230002;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
Call-ID: inv6tel-0001@2001:db8:10::5
CSeq: 1 INVITE
Contact: <sip:001010000000001@[2001:db8:10::5]:50000>;+sip.instance="<urn:gsma:imei:00000000-000000-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
P-Preferred-Identity: <tel:+15551230001>
P-Preferred-Service: urn:urn-7:3gpp-service.ims.icsi.mmtel
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
P-Access-Network-Info: 3GPP-NR-FDD;utran-cell-id-3gpp=00101000001000000001
P-Early-Media: supported
Security-Verify: ipsec-3gpp;prot=esp;mod=trans;spi-c=4098;spi-s=4099;port-c=5102;port-s=5103;alg=hmac-sha-1-96;ealg=null
Require: sec-agree
Proxy-Require: sec-agree
Supported: 100rel,precondition,timer,sec-agree
Session-Expires: 1800
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,OPTIONS,NOTIFY,REFER,INFO
Content-Type: application/sdp
Content-Length: 505

v=0
o=- 1 1 IN IP6 2001:db8:10::5
s=-
c=IN IP6 2001:db8:10::5
b=AS:49
t=0 0
m=audio 50010 RTP/AVP 116 118 100
b=AS:49
b=RS:0
b=RR:1537
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=0
a=rtpmap:118 AMR/8000/1
a=fmtp:118 mode-change-capability=2;max-red=0
a=rtpmap:100 telephone-event/16000
a=fmtp:100 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
a=sendrecv
a=ptime:20
a=maxptime:240
