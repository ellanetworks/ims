INVITE sip:999010000000007@10.46.0.7:6600 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKGVBNCB4FZLUSESWMPGUNDIOCBR
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKE2J33PXQ4V5OJNNOMPNOD5ZLR3
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKYJR7FPIHFTITQVVANI4XEGO7BV
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKQ55TNKVOQPL6OZIOBTZ4RDYJK4
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKGRNVHYQOFGAYB432XH622THRL7
From: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=100699893
To: <tel:15550000007;phone-context=ims.mnc001.mcc999.3gppnetwork.org>
CSeq: 100699886 INVITE
Call-ID: 100699886_3068622352@10.46.0.8
Via: SIP/2.0/TCP 10.46.0.8:41989;branch=z9hG4bK3629178243
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5063;transport=tcp;lr;r2=on;did=FLUIUSQENRDFGD2K;ue>
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5060;lr;r2=on;did=FLUIUSQENRDFGD2K>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=Q6G6GKCZOK2FLXON>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=EVNTE6S5UQASFCPT>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5060;lr;r2=on;did=OFUBLLFGBYHHRK5W>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=OFUBLLFGBYHHRK5W;ue>
Max-Forwards: 65
Contact: <sip:cbcd92dc-db6f-4aea-a207-e26b95efb9f2@10.46.0.8:41989>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Supported: timer,199,100rel,replaces,precondition,from-change,histinfo,tdialog
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Length: 761
Session-Expires: 1800;refresher=uac
P-Called-Party-ID: <tel:+15550000007>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000008>

v=0
o=- 8 1000 IN IP4 10.46.0.8
s=QC VOIP
c=IN IP4 10.46.0.8
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50052 RTP/AVP 104 110 102 108 96 97
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:110 AMR-WB/16000/1
a=fmtp:110 octet-align=1;mode-change-capability=2;max-red=0
a=rtpmap:102 AMR/8000/1
a=fmtp:102 mode-change-capability=2;max-red=0
a=rtpmap:108 AMR/8000/1
a=fmtp:108 octet-align=1;mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=rtpmap:97 telephone-event/8000
a=fmtp:97 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
