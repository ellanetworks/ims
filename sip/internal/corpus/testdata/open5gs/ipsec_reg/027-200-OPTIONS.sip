SIP/2.0 200 OK
Via: SIP/2.0/UDP 10.4.128.21;branch=z9hG4bK2a94.4e92d7a7000000000000000000000000.0
To: <sip:192.168.101.2:5060>;tag=4130316687
From: <sip:pcscf.ims.mnc001.mcc001.3gppnetwork.org:5060>;tag=e2e7adbd78741b945f071315c250103c-cfa69f2f
CSeq: 10 OPTIONS
Call-ID: 33006ec626c23f51-25423@10.4.128.21
Content-Length: 1021
P-Access-Network-Info: 3GPP-E-UTRAN-FDD; utran-cell-id-3gpp=001010001000019B
Contact: <sip:192.168.101.2:5060>
Supported: 100rel,replaces,histinfo,path,timer
Content-Type: application/sdp
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO

v=0
o=- 1065 1000 IN IP4 192.168.101.2
s=QC VOIP
c=IN IP4 192.168.101.2
b=AS:442
b=RS:1200
b=RR:4000
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
m=video 0 RTP/AVP 114 113
b=AS:401
b=RS:600
b=RR:2000
a=rtpmap:114 H264/90000
a=fmtp:114 profile-level-id=42C00C;packetization-mode=1;sar-understood=49;sar-supported=49;sprop-parameter-sets=Z0LADNoPCmgG0KE1,aM4G4g==
a=rtpmap:113 H264/90000
a=fmtp:113 profile-level-id=42C00C;packetization-mode=0;sar-understood=49;sar-supported=49;sprop-parameter-sets=Z0LADNoPCmgG0KE1,aM4G4g==
a=rtcp-fb:* nack
a=rtcp-fb:* nack pli
a=rtcp-fb:* ccm fir
a=rtcp-fb:* ccm tmmbr
a=sendrecv
a=extmap:2 urn:3gpp:video-orientation
a=tcap:1 RTP/AVPF
a=pcfg:1 t=1
