INVITE sip:0398765432100;phone-context=0498765432100@0498765432100;user=phone SIP/2.0
Via: SIP/2.0/UDP 192.168.101.3:6400;branch=z9hG4bK-524287-1---b67029e8d52ecffc;rport;transport=UDP
Max-Forwards: 70
Route: <sip:10.4.128.21:6101;lr>
Route: <sip:orig@scscf.ims.mnc001.mcc001.3gppnetwork.org:6060;lr>
Proxy-Require: sec-agree
Require: sec-agree
Contact: <sip:0498765432100@192.168.101.3:6400>;+sip.instance="<urn:gsma:imei:35622410-483317-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;video
To: <sip:0398765432100;phone-context=0498765432100@0498765432100;user=phone>
From: <tel:0498765432100>;tag=7b3fae13
Call-ID: TF0lR-e_t0o7Jp-w5kq58A..@192.168.101.3
CSeq: 1 INVITE
Session-Expires: 1800
Accept: application/sdp, application/3gpp-ims+xml
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition, gruu, sec-agree
User-Agent: SM-T515-TB4 Samsung IMS 6.0
Security-Verify: ipsec-3gpp;prot=esp;mod=trans;spi-c=102;spi-s=103;port-c=5101;port-s=6101;alg=hmac-sha-1-96;ealg=null
P-Preferred-Identity: <tel:0498765432100>
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
P-Early-Media: supported
P-Preferred-Service: urn:urn-7:3gpp-service.ims.icsi.mmtel
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001000019b
Content-Length: 583

v=0
o=SAMSUNG-IMS-UE 5832785110 0 IN IP4 192.168.101.3
s=SS VOIP
c=IN IP4 192.168.101.3
t=0 0
m=audio 1242 RTP/AVP 116 118 111 110
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=220
a=rtpmap:118 AMR/8000/1
a=fmtp:118 mode-change-capability=2;max-red=220
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=rtpmap:110 telephone-event/8000
a=fmtp:110 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
a=sendrecv
a=ptime:20
a=maxptime:240
