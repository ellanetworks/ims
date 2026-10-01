SIP/2.0 200 OK
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK5a83.832ed30e6a5cc9d3a1b31b3a103be48d.0
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK5a83.366bba725393284b1c919ffb269794e6.0
Via: SIP/2.0/UDP 10.4.128.21:5100;branch=z9hG4bK5a83.3853080bfd6063e9e722ef3b1b5df974.0
Via: SIP/2.0/UDP 192.168.101.3:6400;received=192.168.101.3;branch=z9hG4bK-524287-1---e162b34e1ae61e2d;rport=6401;transport=UDP
Max-Forwards: 70
Require: timer, precondition
Contact: <sip:0398765432100@192.168.101.2:6300;alias=192.168.101.2~6301~1>;+sip.instance="<urn:gsma:imei:35622410-483840-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;video
To: <sip:0398765432100;phone-context=0498765432100@0498765432100;user=phone>;tag=4cbb9622
From: <tel:0498765432100>;tag=7b3fae13
Call-ID: TF0lR-e_t0o7Jp-w5kq58A..@192.168.101.3
CSeq: 3 UPDATE
Session-Expires: 1800;refresher=uac
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition
User-Agent: SM-T515-TB4 Samsung IMS 6.0
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001000019b
Content-Length: 457

v=0
o=SAMSUNG-IMS-UE 925403981 1 IN IP4 192.168.101.2
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
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
