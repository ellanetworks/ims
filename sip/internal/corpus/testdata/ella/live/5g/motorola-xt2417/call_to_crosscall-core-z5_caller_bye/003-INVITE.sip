INVITE sip:2da86196-c8f7-4939-af8b-fbdac4e983f2@10.46.0.8:42225 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKN6XXWOCHRMRMRLUEU5ZU6CTAL4
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKUFPSJ5CCYKXG5KGDEKIKLVJXBD
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKQLPACDM6EEO3IMJDYKIXXN6CL2
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKQ6RZERGJO3FKY64P3D645CDWAW
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKZNOF3MWU6EUWL6ZCH7T2UAJ24L
From: <sip:15550000008@ims.mnc001.mcc001.3gppnetwork.org>;tag=110381187
To: <sip:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>
CSeq: 110381182 INVITE
Call-ID: 110381182_4109868208@10.46.0.9
Via: SIP/2.0/TCP 10.46.0.9:43885;branch=z9hG4bK3439066730
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5063;transport=tcp;lr;r2=on;did=5AX5VLJIYKXJI5TD;ue>
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5060;lr;r2=on;did=5AX5VLJIYKXJI5TD>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=UBVOTASGC3S2Q5JR>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=S3IWEBVY7OHMTTVY>
Record-Route: <sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5060;lr;r2=on;did=7D5MQKY37NSIRTAU>
Record-Route: <sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5063;transport=tcp;lr;r2=on;did=7D5MQKY37NSIRTAU;ue>
Max-Forwards: 65
Contact: <sip:a87a2e9a-4c32-439f-afc0-825ea2f8b236@10.46.0.9:43885>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Supported: timer,199,100rel,replaces,precondition,from-change,histinfo,tdialog
User-Agent: PRD-IR92/12 term-Motorola/XT2417-1-V1UFN35H.193-20 device-type/smart-phone
Content-Length: 833
Session-Expires: 1800
P-Called-Party-ID: <tel:+15550000005>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000008@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000008>

v=0
o=- 1 1000 IN IP4 10.46.0.9
s=QC VOIP
c=IN IP4 10.46.0.9
b=AS:145
b=RS:600
b=RR:2000
t=0 0
m=audio 50032 RTP/AVP 106 126 127 99 97 96 98
b=AS:145
b=RS:600
b=RR:2000
a=rtpmap:106 EVS/16000/1
a=fmtp:106 br=5.9-128;bw=nb-swb;ch-aw-recv=2;max-red=0
a=rtpmap:126 EVS/16000/1
a=fmtp:126 br=5.9-24.4;bw=nb-swb;ch-aw-recv=2;max-red=0
a=rtpmap:127 EVS/16000/1
a=fmtp:127 br=5.9-24.4;bw=nb-swb;ch-aw-recv=2;max-red=0
a=rtpmap:99 AMR-WB/16000/1
a=fmtp:99 mode-change-capability=2;max-red=0
a=rtpmap:97 AMR/8000/1
a=fmtp:97 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=rtpmap:98 telephone-event/8000
a=fmtp:98 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
