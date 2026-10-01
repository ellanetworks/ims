SIP/2.0 200 OK
Record-Route: <sip:mt@10.4.128.21;lr=on;ftag=b29e8e10;rm=7;did=0f5.5771>,<sip:mt@10.4.128.21:6060;lr=on;ftag=b29e8e10;did=0f5.ffa1>,<sip:mo@10.4.128.21:6060;lr=on;ftag=b29e8e10;did=0f5.efa1>,<sip:mo@10.4.128.21;r2=on;lr=on;ftag=b29e8e10;rm=8;did=0f5.3771>,<sip:mo@10.4.128.21:6106;transport=tcp;r2=on;lr=on;ftag=b29e8e10;rm=8;did=0f5.3771>
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK826f.08a2db7e44e7f758cbb0f50c7a4fff12.1,SIP/2.0/UDP 10.4.128.21:4060;branch=z9hG4bK826f.616718c9c228c06ad6dc0a1ce2ba2456.1,SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK826f.7877e5600d25e4defa00a55f10c2870b.0,SIP/2.0/UDP 10.4.128.21;branch=z9hG4bK826f.57bcd4f8583db0807b4a9aece35f87a6.0;i=c,SIP/2.0/TCP 192.168.101.6:6700;received=192.168.101.6;branch=z9hG4bK-524287-1---e4d6b60a70aa2dc6;rport=6701;transport=TCP
To: <sip:0598765432100;phone-context=ims.mnc001.mcc262.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=4133807704
From: <sip:001011234567893@ims.mnc001.mcc001.3gppnetwork.org>;tag=b29e8e10
Call-ID: UeE-Kc-iNlv7QvhZbppI2Q..@192.168.101.6
CSeq: 1 INVITE
Session-Expires: 1800;refresher=uac
Content-Type: application/sdp
P-Access-Network-Info: 3GPP-E-UTRAN-FDD; utran-cell-id-3gpp=001010001000019B
Content-Length: 353
Privacy: id
Contact: <sip:192.168.101.8:5060;alias=192.168.101.8~5060~1>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";video;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Supported: 100rel,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Require: timer
C-Params: +g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";video;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting

v=0
o=- 1076 1000 IN IP4 10.4.128.21
s=QC VOIP
c=IN IP4 10.4.128.21
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 30346 RTP/AVP 116 111
b=AS:41
b=RS:600
b=RR:2000
a=maxptime:240
a=rtpmap:116 AMR-WB/16000/1
a=rtpmap:111 telephone-event/16000
a=fmtp:116 mode-change-capability=2;max-red=0
a=fmtp:111 0-15
a=sendrecv
a=rtcp:30347
a=ptime:20
