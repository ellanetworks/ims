INVITE sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone SIP/2.0
Via: SIP/2.0/TCP 10.0.0.2:38873;branch=z9hG4bKLRWYEUC4NQH6PVNXVBM3VB3QBM;rport
Content-Length: 441
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=62156179;spi-s=149231997;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
Require: sec-agree
Proxy-Require: sec-agree
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100010000001
Route: <sip:10.0.0.1:5063;lr>
Route: <sip:orig-1@scscf.ims.mnc001.mcc001.3gppnetwork.org:5080;lr>
Max-Forwards: 70
From: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>;tag=HSE7SBOTEW6WSVPZ2JIJ4LBU6T
To: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>
Call-ID: 45731f74-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2
CSeq: 1 INVITE
Contact: <sip:4540d1d8-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2:38873>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
P-Preferred-Identity: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>
P-Preferred-Service: urn:urn-7:3gpp-service.ims.icsi.mmtel
P-Early-Media: supported
Supported: 100rel, timer
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE
Accept: application/sdp, application/3gpp-ims+xml
Session-Expires: 1800
Content-Type: application/sdp

v=0
o=- 117379067674627 0 IN IP4 10.0.0.2
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
