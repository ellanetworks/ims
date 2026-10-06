INVITE sip:1ca9266b-5791-f0ca-9a23-92da316d71a9@10.46.0.3:49165 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKDY2P5AO5XPHCFETSWSK5KVBP35
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKL2M2ZPZH5ONXTOWIZLSGTZIRDY
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKTWZBZ2U3GUCCOLRIIF73BHFUGC
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKHRPYWPZRV7IS2MYQYWUXDNDTT6
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKY573GCG55X5HSW5BGEG27POCKR
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=103428407
To: <tel:15550000002;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
CSeq: 103428400 INVITE
Call-ID: 103428400_1759955408@10.46.0.2
Via: SIP/2.0/TCP 10.46.0.2:43300;branch=z9hG4bK1914793514
Record-Route: <sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5063;transport=tcp;lr;r2=on;ob;did=GVR3JWODM4TFC7BI;ue>
Record-Route: <sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5060;lr;r2=on;ob;did=GVR3JWODM4TFC7BI>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=RKCP4XAE5ZE445PP>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=CX45PVPUGXCTRVNS>
Record-Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5060;lr;r2=on;did=27E46R75JD77JBKM>
Record-Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5063;transport=tcp;lr;r2=on;did=27E46R75JD77JBKM;ue>
Max-Forwards: 65
Contact: <sip:b394eba0-c3ed-4e54-adf4-83d317ca2979@10.46.0.2:43300>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Supported: timer,199,100rel,replaces,precondition,from-change,histinfo,tdialog
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Length: 762
Session-Expires: 1800;refresher=uac
P-Called-Party-ID: <tel:+15550000002>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000005>

v=0
o=- 14 1000 IN IP4 10.46.0.2
s=QC VOIP
c=IN IP4 10.46.0.2
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50032 RTP/AVP 104 110 102 108 96 97
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
