INVITE sip:001011234567893@192.168.101.2:6300 SIP/2.0
Record-Route: <sip:mt@10.4.128.21:5100;r2=on;lr=on;ftag=7b3fae13;rm=7;did=078.754>
Record-Route: <sip:mt@10.4.128.21;r2=on;lr=on;ftag=7b3fae13;rm=7;did=078.754>
Record-Route: <sip:mt@10.4.128.21:6060;lr=on;ftag=7b3fae13;did=078.5c52>
Record-Route: <sip:mo@10.4.128.21:6060;lr=on;ftag=7b3fae13;did=078.4c52>
Record-Route: <sip:mo@10.4.128.21:6101;lr=on;ftag=7b3fae13;rm=8;did=078.654>
Via: SIP/2.0/UDP 10.4.128.21:5100;branch=z9hG4bK7a83.008e410389c0f1c6e7ee5ccf4dd1f01f.0
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK7a83.33545547892f3c380b32f2667299e8dd.0
Via: SIP/2.0/UDP 10.4.128.21:4060;branch=z9hG4bK7a83.10ebe26c90727525f474ad6fa5e22c75.1
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK7a83.97f95e8b7e944ba2bd0e3f19e23e913b.0
Via: SIP/2.0/UDP 10.4.128.21:6101;branch=z9hG4bK7a83.64fb9469779e2867fdb139eecff6c335.0
Via: SIP/2.0/UDP 192.168.101.3:6400;received=192.168.101.3;branch=z9hG4bK-524287-1---b67029e8d52ecffc;rport=6401;transport=UDP
Max-Forwards: 65
Contact: <sip:0498765432100@192.168.101.3:6400;alias=192.168.101.3~6401~1>;+sip.instance="<urn:gsma:imei:35622410-483317-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;video
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
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
P-Early-Media: supported
P-Preferred-Service: urn:urn-7:3gpp-service.ims.icsi.mmtel
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001000019b
Content-Length: 594
P-Charging-Vector: icid-value=4956537F000001547600007C00000000; icid-generated-at=10.4.128.21
P-Visited-Network-ID: ims.mnc001.mcc001.3gppnetwork.org
P-Asserted-Identity: <sip:0498765432100@<null>>
Supported: sec-agree
Security-Server: ipsec-3gpp;prot=esp;mod=trans;spi-c=100;spi-s=101;port-c=5100;port-s=6100;alg=hmac-sha-1-96;ealg=null
Supported: sec-agree
Security-Server: ipsec-3gpp;prot=esp;mod=trans;spi-c=100;spi-s=101;port-c=5100;port-s=6100;alg=hmac-sha-1-96;ealg=null

v=0
o=SAMSUNG-IMS-UE 5832785110 0 IN IP4 10.4.128.21
s=SS VOIP
c=IN IP4 10.4.128.21
t=0 0
m=audio 30626 RTP/AVP 116 118 111 110
b=AS:41
b=RS:512
b=RR:1537
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
a=maxptime:240
a=rtpmap:116 AMR-WB/16000/1
a=rtpmap:118 AMR/8000/1
a=rtpmap:111 telephone-event/16000
a=rtpmap:110 telephone-event/8000
a=fmtp:116 mode-change-capability=2;max-red=220
a=fmtp:118 mode-change-capability=2;max-red=220
a=fmtp:111 0-15
a=fmtp:110 0-15
a=sendrecv
a=rtcp:30627
a=ptime:20
