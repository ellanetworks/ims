UPDATE sip:4542e7f2-bf70-11f1-81ff-2bcf73babbc9@10.0.0.3:35047 SIP/2.0
Via: SIP/2.0/TCP 10.0.0.2:38873;branch=z9hG4bKYAM2QPB7QH2Y7U6BSATCOWPM7Q;rport
Route: <sip:7qi7n3eiqts4aak3xd3aop3cjh@10.0.0.1:5063;transport=tcp;lr;r2=on;did=DWFKBYMFP6K6MZCN;ue>, <sip:7qi7n3eiqts4aak3xd3aop3cjh@10.0.0.1:5060;lr;r2=on;did=DWFKBYMFP6K6MZCN>, <sip:mo@10.0.0.1:5080;lr;did=Y4MG2YEKOYNRRCID>, <sip:mt@10.0.0.1:5080;lr;did=OMDLE7NNWM5OPZ5U>, <sip:dmigbowgjhlyfhiyff53dy5zs3@10.0.0.1:5060;lr;r2=on;did=PNR5WJJ47OKU2ZFE>, <sip:dmigbowgjhlyfhiyff53dy5zs3@10.0.0.1:5063;transport=tcp;lr;r2=on;did=PNR5WJJ47OKU2ZFE;ue>
Max-Forwards: 70
From: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>;tag=NF6P3SIPZFTE7O6C7FX4UYAMCO
To: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=SUMK2J5KZ2HXSXFRXVKS5FQGKS
Call-ID: 459358e3-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2
CSeq: 3 UPDATE
Content-Length: 565
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=62156179;spi-s=149231997;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
Require: sec-agree
Proxy-Require: sec-agree
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100010000001
Contact: <sip:4540d1d8-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2:38873>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE
Content-Type: application/sdp
Require: precondition
Supported: 100rel, timer, precondition

v=0
o=- 117379067674629 1 IN IP4 10.0.0.2
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
