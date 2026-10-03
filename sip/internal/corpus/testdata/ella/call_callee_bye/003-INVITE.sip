INVITE sip:656c296f-bf75-11f1-86b4-4d6ade81ba5e@10.0.0.3:46761 SIP/2.0
Via: SIP/2.0/TCP 10.0.0.1:5063;branch=z9hG4bKBYVIU3HFOKWDGHIBZRTQ3LKTD3
Via: SIP/2.0/UDP 10.0.0.1:5080;branch=z9hG4bKYFRRPZMAIEMXYMRTITQO6F4MG3
Via: SIP/2.0/UDP 10.0.0.1:5070;branch=z9hG4bKJIKOUA2H6FMUNF2D3HZRJ3M72M
Via: SIP/2.0/UDP 10.0.0.1:5080;branch=z9hG4bKLDL42RXFSLBTXXB76OZDDQRMOT
Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKPN6C3X2XVSBXU63W4OZW3VHAXL
Via: SIP/2.0/TCP 10.0.0.2:38889;branch=z9hG4bK2OLBAFVOIKOQSKDZZ6NYBEYHIT;rport=44803;received=10.0.0.2
Record-Route: <sip:zfh2lgtqhm5cnong5mbtqesooq@10.0.0.1:5063;transport=tcp;lr;r2=on;did=3L4C4RBGABTKH2QV;ue>
Record-Route: <sip:zfh2lgtqhm5cnong5mbtqesooq@10.0.0.1:5060;lr;r2=on;did=3L4C4RBGABTKH2QV>
Record-Route: <sip:mt@10.0.0.1:5080;lr;did=A2I3SFIQA6ZE4743>
Record-Route: <sip:mo@10.0.0.1:5080;lr;did=N7LFTNTPC2NXBI26>
Record-Route: <sip:fch555hos5mgc3rtk3lovlciov@10.0.0.1:5060;lr;r2=on;did=FZSWJSYQHNKIQKTE>
Record-Route: <sip:fch555hos5mgc3rtk3lovlciov@10.0.0.1:5063;transport=tcp;lr;r2=on;did=FZSWJSYQHNKIQKTE;ue>
Content-Length: 441
Max-Forwards: 65
From: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>;tag=2HF5ZK5D4KAQRZ6JB3PGHZITU7
To: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>
Call-ID: 65b27b31-bf75-11f1-86b4-4d6ade81ba5e@10.0.0.2
CSeq: 1 INVITE
Contact: <sip:656a56a9-bf75-11f1-86b4-4d6ade81ba5e@10.0.0.2:38889>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Supported: 100rel, timer, 199
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE
Accept: application/sdp, application/3gpp-ims+xml
Session-Expires: 1800
Content-Type: application/sdp
P-Called-Party-ID: <tel:+15550002>
P-Early-Media: supported
P-Asserted-Identity: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550001>

v=0
o=- 117379211919363 0 IN IP4 10.0.0.2
s=-
c=IN IP4 10.0.0.2
t=0 0
m=audio 40002 RTP/AVP 116 118 111 110
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
a=sendrecv
a=ptime:20
a=maxptime:240
