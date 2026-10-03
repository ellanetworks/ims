INVITE sip:4542e7f2-bf70-11f1-81ff-2bcf73babbc9@10.0.0.3:35047 SIP/2.0
Via: SIP/2.0/TCP 10.0.0.1:5063;branch=z9hG4bKS2LYSI3DS7RTNFV5QJZRJTLTKX
Via: SIP/2.0/UDP 10.0.0.1:5080;branch=z9hG4bKCISNYODQQEAWI33BWL4JFNKXGZ
Via: SIP/2.0/UDP 10.0.0.1:5070;branch=z9hG4bKBV6WQRTBDCHSWXFN2IW6LLBK4G
Via: SIP/2.0/UDP 10.0.0.1:5080;branch=z9hG4bKLSK6Q773QJRQH6ZQ2Y3CHM4WLQ
Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKT5Y7767XIXU3RV4O763VFATI4T
Via: SIP/2.0/TCP 10.0.0.2:38873;branch=z9hG4bKVMFASSMSSZ72WVZSMCN72VYZUK;rport=45067;received=10.0.0.2
Record-Route: <sip:dmigbowgjhlyfhiyff53dy5zs3@10.0.0.1:5063;transport=tcp;lr;r2=on;did=PNR5WJJ47OKU2ZFE;ue>
Record-Route: <sip:dmigbowgjhlyfhiyff53dy5zs3@10.0.0.1:5060;lr;r2=on;did=PNR5WJJ47OKU2ZFE>
Record-Route: <sip:mt@10.0.0.1:5080;lr;did=OMDLE7NNWM5OPZ5U>
Record-Route: <sip:mo@10.0.0.1:5080;lr;did=Y4MG2YEKOYNRRCID>
Record-Route: <sip:7qi7n3eiqts4aak3xd3aop3cjh@10.0.0.1:5060;lr;r2=on;did=DWFKBYMFP6K6MZCN>
Record-Route: <sip:7qi7n3eiqts4aak3xd3aop3cjh@10.0.0.1:5063;transport=tcp;lr;r2=on;did=DWFKBYMFP6K6MZCN;ue>
Content-Length: 560
Max-Forwards: 65
From: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>;tag=NF6P3SIPZFTE7O6C7FX4UYAMCO
To: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>
Call-ID: 459358e3-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2
CSeq: 1 INVITE
Contact: <sip:4540d1d8-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2:38873>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Supported: 100rel, timer, precondition
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE
Accept: application/sdp, application/3gpp-ims+xml
Session-Expires: 1800
Content-Type: application/sdp
P-Called-Party-ID: <tel:+15550002>
P-Early-Media: supported
P-Asserted-Identity: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550001>

v=0
o=- 117379067674629 0 IN IP4 10.0.0.2
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
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
a=sendrecv
a=ptime:20
a=maxptime:240
