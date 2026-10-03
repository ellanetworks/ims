SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.0.0.2:34253;branch=z9hG4bKJT3NCZEDLGMG3ZHI5L3USLO5ZO;rport=38225;received=10.0.0.2
Record-Route: <sip:mt@10.0.0.1:5080;lr;did=NTSNXLUHGL6PY5EB>
Record-Route: <sip:mo@10.0.0.1:5080;lr;did=Q7XI4HBWGPYVE24E>
From: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>;tag=NHPSGW3YBBMNXVJDHW53XDVNGU
To: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=TQJTDMFPY33VWNCTMH3QOXSNZH
Call-ID: 4603afff-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2
CSeq: 3 UPDATE
Content-Length: 435
Contact: <sip:45c5aa86-bf70-11f1-81ff-2bcf73babbc9@10.0.0.3:33445>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE
Content-Type: application/sdp
Require: precondition

v=0
o=- 117379067674634 1 IN IP4 10.0.0.3
s=-
c=IN IP4 10.0.0.3
t=0 0
m=audio 40002 RTP/AVP 116 111
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
