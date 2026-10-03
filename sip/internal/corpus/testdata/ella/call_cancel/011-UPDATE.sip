UPDATE sip:45c5aa86-bf70-11f1-81ff-2bcf73babbc9@10.0.0.3:33445 SIP/2.0
Via: SIP/2.0/TCP 10.0.0.2:34253;branch=z9hG4bKJT3NCZEDLGMG3ZHI5L3USLO5ZO;rport
Route: <sip:lcvkhad6e5wvnlost4ata7aziz@10.0.0.1:5063;transport=tcp;lr;r2=on;did=QEDC7Q3R6DODPSC2;ue>, <sip:lcvkhad6e5wvnlost4ata7aziz@10.0.0.1:5060;lr;r2=on;did=QEDC7Q3R6DODPSC2>, <sip:mo@10.0.0.1:5080;lr;did=Q7XI4HBWGPYVE24E>, <sip:mt@10.0.0.1:5080;lr;did=NTSNXLUHGL6PY5EB>, <sip:vnr7bmqfxnmbaexcy6fhcmncpf@10.0.0.1:5060;lr;r2=on;did=X7OUUNZPLUMJ362N>, <sip:vnr7bmqfxnmbaexcy6fhcmncpf@10.0.0.1:5063;transport=tcp;lr;r2=on;did=X7OUUNZPLUMJ362N;ue>
Max-Forwards: 70
From: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>;tag=NHPSGW3YBBMNXVJDHW53XDVNGU
To: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=TQJTDMFPY33VWNCTMH3QOXSNZH
Call-ID: 4603afff-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2
CSeq: 3 UPDATE
Content-Length: 565
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=122230114;spi-s=108696944;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
Require: sec-agree
Proxy-Require: sec-agree
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100010000001
Contact: <sip:45c429ec-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2:34253>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE
Content-Type: application/sdp
Require: precondition
Supported: 100rel, timer, precondition

v=0
o=- 117379067674633 1 IN IP4 10.0.0.2
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
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=ptime:20
a=maxptime:240
