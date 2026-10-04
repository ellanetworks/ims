INVITE sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKAAA5VLS3UTOFGDAGLJAXYG6R3L
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKBPO4X4ZNZAMS3ED36HEUUQ7W3G
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKEZES4ZM2XPPSK624WFWPZFWMPE
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK4TZ5TIZ6EIAZLZQWFBPMSMM2NT
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKCQLY4JASNSTJXJ3XIT6U3QSDM4
From: <sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org>;tag=714593
To: <sip:15550000004;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>
CSeq: 714586 INVITE
Call-ID: 714586_4111201712@10.46.0.14
Via: SIP/2.0/TCP 10.46.0.14:40265;branch=z9hG4bK433122552
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=WBHZ5CXBTNINNCXK;ue>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=WBHZ5CXBTNINNCXK>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=CN5VYNJ7TIKGNMSI>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=7OAQAI477SM3ORMU>
Record-Route: <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;lr;r2=on;did=5NBGRQBGJZQUKTP2>
Record-Route: <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;transport=tcp;lr;r2=on;did=5NBGRQBGJZQUKTP2;ue>
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
o=- 1 1000 IN IP4 10.46.0.14
s=QC VOIP
c=IN IP4 10.46.0.14
b=AS:145
b=RS:600
b=RR:2000
t=0 0
m=audio 50058 RTP/AVP 106 126 127 99 97 96 98
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
