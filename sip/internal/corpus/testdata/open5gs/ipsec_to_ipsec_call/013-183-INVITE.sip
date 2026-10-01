SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.4.128.21:5100;branch=z9hG4bK7a83.008e410389c0f1c6e7ee5ccf4dd1f01f.0;transport=TCP
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK7a83.33545547892f3c380b32f2667299e8dd.0
Via: SIP/2.0/UDP 10.4.128.21:4060;branch=z9hG4bK7a83.10ebe26c90727525f474ad6fa5e22c75.1
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK7a83.97f95e8b7e944ba2bd0e3f19e23e913b.0
Via: SIP/2.0/UDP 10.4.128.21:6101;branch=z9hG4bK7a83.64fb9469779e2867fdb139eecff6c335.0
Via: SIP/2.0/UDP 192.168.101.3:6400;received=192.168.101.3;branch=z9hG4bK-524287-1---b67029e8d52ecffc;rport=6401;transport=UDP
Max-Forwards: 70
Record-Route: <sip:mt@10.4.128.21:5100;r2=on;lr=on;ftag=7b3fae13;rm=7;did=078.754>
Record-Route: <sip:mt@10.4.128.21;r2=on;lr=on;ftag=7b3fae13;rm=7;did=078.754>
Record-Route: <sip:mt@10.4.128.21:6060;lr=on;ftag=7b3fae13;did=078.5c52>
Record-Route: <sip:mo@10.4.128.21:6060;lr=on;ftag=7b3fae13;did=078.4c52>
Record-Route: <sip:mo@10.4.128.21:6101;lr=on;ftag=7b3fae13;rm=8;did=078.654>
Require: precondition, 100rel
Contact: <sip:0398765432100@192.168.101.2:6300>;+sip.instance="<urn:gsma:imei:35622410-483840-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;video
To: <sip:0398765432100;phone-context=0498765432100@0498765432100;user=phone>;tag=4cbb9622
From: <tel:0498765432100>;tag=7b3fae13
Call-ID: TF0lR-e_t0o7Jp-w5kq58A..@192.168.101.3
CSeq: 1 INVITE
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition
User-Agent: SM-T515-TB4 Samsung IMS 6.0
RSeq: 1
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001000019b
Content-Length: 477

v=0
o=SAMSUNG-IMS-UE 925403981 0 IN IP4 192.168.101.2
s=SS VOIP
c=IN IP4 192.168.101.2
t=0 0
m=audio 1234 RTP/AVP 116 111
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=220
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=conf:qos remote sendrecv
