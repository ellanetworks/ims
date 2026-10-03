UPDATE sip:656c296f-bf75-11f1-86b4-4d6ade81ba5e@10.0.0.3:46761 SIP/2.0
Via: SIP/2.0/TCP 10.0.0.2:38889;branch=z9hG4bKA7MZ7C57CLLNFOXNZMXQLMH5SW;rport
Route: <sip:fch555hos5mgc3rtk3lovlciov@10.0.0.1:5063;transport=tcp;lr;r2=on;did=APZ5OSQXGKMJ3GN7;ue>, <sip:fch555hos5mgc3rtk3lovlciov@10.0.0.1:5060;lr;r2=on;did=APZ5OSQXGKMJ3GN7>, <sip:mo@10.0.0.1:5080;lr;did=SFJONNCYBBFMED36>, <sip:mt@10.0.0.1:5080;lr;did=FTRICVE44SP7L6NB>, <sip:zfh2lgtqhm5cnong5mbtqesooq@10.0.0.1:5060;lr;r2=on;did=Z4VMEM24F5VMZ4VK>, <sip:zfh2lgtqhm5cnong5mbtqesooq@10.0.0.1:5063;transport=tcp;lr;r2=on;did=Z4VMEM24F5VMZ4VK;ue>
Max-Forwards: 70
From: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>;tag=RC6EN5FGANJVFC6BSN4Q4QOSOW
To: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=Z754NVCUTCJLSDFBU7CSUROVY2
Call-ID: 65e12d93-bf75-11f1-86b4-4d6ade81ba5e@10.0.0.2
CSeq: 3 UPDATE
Content-Length: 565
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=239075227;spi-s=42329523;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
Require: sec-agree
Proxy-Require: sec-agree
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100010000001
Contact: <sip:656a56a9-bf75-11f1-86b4-4d6ade81ba5e@10.0.0.2:38889>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE
Content-Type: application/sdp
Require: precondition
Supported: 100rel, timer, precondition

v=0
o=- 117379211919365 1 IN IP4 10.0.0.2
s=-
c=IN IP4 10.0.0.2
t=0 0
m=audio 40004 RTP/AVP 116 118 111 110
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
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=ptime:20
a=maxptime:240
