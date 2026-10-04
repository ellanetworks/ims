INVITE sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK6ERZALGTYLEDG3ZGDBMUCWF75Q
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKKOOSNZPSUGDMRKPADBSAYUFQLQ
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKNC3JV6RX3MKWM6V3EAI2MYGPKK
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKB7OMFWSBKLSCWJPQUMBOXLD52I
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKQJQJWCOK2VRNSTTD22NEBN6YOS
From: <sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org>;tag=877628
To: <sip:15550000004;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>
CSeq: 877621 INVITE
Call-ID: 877621_4111242528@10.46.0.14
Via: SIP/2.0/TCP 10.46.0.14:40265;branch=z9hG4bK3637598337
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=SDDNIER5RJ7WQSTR;ue>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=SDDNIER5RJ7WQSTR>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=4JPO4OY5BD5KRJD2>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=T45IOCLIMXJ7ZJJC>
Record-Route: <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;lr;r2=on;did=MEC3I4VACMXMXODM>
Record-Route: <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;transport=tcp;lr;r2=on;did=MEC3I4VACMXMXODM;ue>
Max-Forwards: 65
Contact: <sip:59ca0608-182c-4e87-aed8-aa1e2a898c87@10.46.0.14:40265>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Supported: timer,199,100rel,replaces,precondition,from-change,histinfo,tdialog
User-Agent: PRD-IR92/12 term-Motorola/XT2417-1-V1UFN35H.193-20 device-type/smart-phone
Content-Length: 835
Session-Expires: 1800
P-Called-Party-ID: <tel:+15550000004>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000001>

v=0
o=- 4 1000 IN IP4 10.46.0.14
s=QC VOIP
c=IN IP4 10.46.0.14
b=AS:145
b=RS:600
b=RR:2000
t=0 0
m=audio 50030 RTP/AVP 106 126 127 99 97 96 98
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
