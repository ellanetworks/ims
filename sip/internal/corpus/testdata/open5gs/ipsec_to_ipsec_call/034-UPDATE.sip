UPDATE sip:0398765432100@192.168.101.2:6300;alias=10.4.128.21~6060~1;alias=192.168.101.2~6301~1 SIP/2.0
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK5a83.832ed30e6a5cc9d3a1b31b3a103be48d.0
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK5a83.366bba725393284b1c919ffb269794e6.0
Via: SIP/2.0/UDP 10.4.128.21:5100;branch=z9hG4bK5a83.3853080bfd6063e9e722ef3b1b5df974.0
Via: SIP/2.0/UDP 192.168.101.3:6400;received=192.168.101.3;branch=z9hG4bK-524287-1---e162b34e1ae61e2d;rport=6401;transport=UDP
Max-Forwards: 67
Route: <sip:mt@10.4.128.21;r2=on;lr=on;ftag=7b3fae13;rm=7;did=078.754>
Route: <sip:mt@10.4.128.21:5100;r2=on;lr=on;ftag=7b3fae13;rm=7;did=078.754>
Proxy-Require: sec-agree
Require: precondition, sec-agree
Contact: <sip:0498765432100@192.168.101.3:6400;alias=192.168.101.3~6401~1>;+sip.instance="<urn:gsma:imei:35622410-483317-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;video
To: <sip:0398765432100;phone-context=0498765432100@0498765432100;user=phone>;tag=4cbb9622
From: <tel:0498765432100>;tag=7b3fae13
Call-ID: TF0lR-e_t0o7Jp-w5kq58A..@192.168.101.3
CSeq: 3 UPDATE
Session-Expires: 1800;refresher=uac
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition, sec-agree
User-Agent: SM-T515-TB4 Samsung IMS 6.0
Security-Verify: ipsec-3gpp;prot=esp;mod=trans;spi-c=102;spi-s=103;port-c=5101;port-s=6101;alg=hmac-sha-1-96;ealg=null
P-Preferred-Identity: <tel:0498765432100>
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001000019b
Content-Length: 467
Supported: sec-agree
Security-Server: ipsec-3gpp;prot=esp;mod=trans;spi-c=100;spi-s=101;port-c=5100;port-s=6100;alg=hmac-sha-1-96;ealg=null

v=0
o=SAMSUNG-IMS-UE 5832785110 1 IN IP4 192.168.101.3
s=SS VOIP
c=IN IP4 10.4.128.21
t=0 0
m=audio 30626 RTP/AVP 116 111
b=AS:41
b=RS:512
b=RR:1537
a=maxptime:240
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=rtpmap:116 AMR-WB/16000/1
a=rtpmap:111 telephone-event/16000
a=fmtp:116 mode-change-capability=2;max-red=220
a=fmtp:111 0-15
a=sendrecv
a=rtcp:30627
a=ptime:20
