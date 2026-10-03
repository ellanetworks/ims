SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.0.0.1:5063;branch=z9hG4bK6OU7MC4X4I26UNFNBNV2TIW2LH
Via: SIP/2.0/UDP 10.0.0.1:5080;branch=z9hG4bK5RHMJ53FV55JYIJLIZFXE6G6WF
Via: SIP/2.0/UDP 10.0.0.1:5070;branch=z9hG4bKKTKGP374IOEYUXJK3TDKYCT3IO
Via: SIP/2.0/UDP 10.0.0.1:5080;branch=z9hG4bKMORVPRRKDGAQ2EHJ6BLOL62WPP
Via: SIP/2.0/UDP 10.0.0.1:5060;branch=z9hG4bKHLO2XR7UFB4MKYKMEHAA2KRU2L
Via: SIP/2.0/TCP 10.0.0.2:34253;branch=z9hG4bKAV4K5HH6JF7JUTE7WJHJPCNG5A;rport=38225;received=10.0.0.2
Record-Route: <sip:vnr7bmqfxnmbaexcy6fhcmncpf@10.0.0.1:5063;transport=tcp;lr;r2=on;did=X7OUUNZPLUMJ362N;ue>
Record-Route: <sip:vnr7bmqfxnmbaexcy6fhcmncpf@10.0.0.1:5060;lr;r2=on;did=X7OUUNZPLUMJ362N>
Record-Route: <sip:mt@10.0.0.1:5080;lr;did=NTSNXLUHGL6PY5EB>
Record-Route: <sip:mo@10.0.0.1:5080;lr;did=Q7XI4HBWGPYVE24E>
Record-Route: <sip:lcvkhad6e5wvnlost4ata7aziz@10.0.0.1:5060;lr;r2=on;did=QEDC7Q3R6DODPSC2>
Record-Route: <sip:lcvkhad6e5wvnlost4ata7aziz@10.0.0.1:5063;transport=tcp;lr;r2=on;did=QEDC7Q3R6DODPSC2;ue>
From: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>;tag=NHPSGW3YBBMNXVJDHW53XDVNGU
To: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=TQJTDMFPY33VWNCTMH3QOXSNZH
Call-ID: 4603afff-bf70-11f1-81ff-2bcf73babbc9@10.0.0.2
CSeq: 1 INVITE
Content-Length: 455
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100010000001
Contact: <sip:45c5aa86-bf70-11f1-81ff-2bcf73babbc9@10.0.0.3:33445>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE
Supported: 100rel, timer, precondition
Content-Type: application/sdp
Require: precondition, 100rel
RSeq: 837482279

v=0
o=- 117379067674634 0 IN IP4 10.0.0.3
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
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=conf:qos remote sendrecv
a=sendrecv
a=ptime:20
a=maxptime:240
