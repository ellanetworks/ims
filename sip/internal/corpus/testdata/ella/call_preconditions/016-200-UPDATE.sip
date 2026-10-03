SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.0.0.2:38889;branch=z9hG4bKA7MZ7C57CLLNFOXNZMXQLMH5SW;rport=44803;received=10.0.0.2
Record-Route: <sip:zfh2lgtqhm5cnong5mbtqesooq@10.0.0.1:5063;transport=tcp;lr;r2=on;did=Z4VMEM24F5VMZ4VK;ue>
Record-Route: <sip:zfh2lgtqhm5cnong5mbtqesooq@10.0.0.1:5060;lr;r2=on;did=Z4VMEM24F5VMZ4VK>
Record-Route: <sip:mt@10.0.0.1:5080;lr;did=FTRICVE44SP7L6NB>
Record-Route: <sip:mo@10.0.0.1:5080;lr;did=SFJONNCYBBFMED36>
Record-Route: <sip:fch555hos5mgc3rtk3lovlciov@10.0.0.1:5060;lr;r2=on;did=APZ5OSQXGKMJ3GN7>
Record-Route: <sip:fch555hos5mgc3rtk3lovlciov@10.0.0.1:5063;transport=tcp;lr;r2=on;did=APZ5OSQXGKMJ3GN7;ue>
From: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>;tag=RC6EN5FGANJVFC6BSN4Q4QOSOW
To: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=Z754NVCUTCJLSDFBU7CSUROVY2
Call-ID: 65e12d93-bf75-11f1-86b4-4d6ade81ba5e@10.0.0.2
CSeq: 3 UPDATE
Content-Length: 435
Contact: <sip:656c296f-bf75-11f1-86b4-4d6ade81ba5e@10.0.0.3:46761>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE
Content-Type: application/sdp
Require: precondition

v=0
o=- 117379211919366 1 IN IP4 10.0.0.3
s=-
c=IN IP4 10.0.0.3
t=0 0
m=audio 40004 RTP/AVP 116 111
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=220
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=ptime:20
a=maxptime:240
