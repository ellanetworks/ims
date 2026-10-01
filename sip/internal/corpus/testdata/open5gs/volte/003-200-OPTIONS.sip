SIP/2.0 200 OK
Via: SIP/2.0/UDP 10.4.128.21;branch=z9hG4bK2a5b.29ac4ab3000000000000000000000000.0
To: <sip:192.168.101.3:5060>;tag=401179810
From: <sip:pcscf.ims.mnc001.mcc001.3gppnetwork.org:5060>;tag=e2e7adbd78741b945f071315c250103c-b1b5
CSeq: 10 OPTIONS
Call-ID: 29f22d59735449fd-27067@10.4.128.21
Content-Length: 465
P-Access-Network-Info: 3GPP-E-UTRAN-FDD; utran-cell-id-3gpp=001010001000019C
Contact: <sip:192.168.101.3:5060>
Supported: 100rel,replaces,histinfo,path,timer
Content-Type: application/sdp
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO

v=0
o=- 1078 1000 IN IP4 192.168.101.3
s=QC VOIP
c=IN IP4 192.168.101.3
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 0 RTP/AVP 104 102 96 97
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:102 AMR/8000/1
a=fmtp:102 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=rtpmap:97 telephone-event/8000
a=fmtp:97 0-15
a=sendrecv
a=maxptime:240
a=ptime:20
