SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.46.0.14:40265;branch=z9hG4bK1444696567
From: <sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org>;tag=1927058
To: <sip:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=zFJEGVPSyo
CSeq: 1927053 INVITE
Call-ID: 1927053_4111230880@10.46.0.14
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=URACXRURYM4GEOMA;lr;r2=on;transport=tcp;ue>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=URACXRURYM4GEOMA;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=CAVDO2FD46F6RGJT;lr>, <sip:mo@10.80.0.5:5080;did=6LG66HU6EAC2ZD2C;lr>, <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;did=YVJ2GKBMFEHHVT57;lr;r2=on>, <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;did=YVJ2GKBMFEHHVT57;lr;r2=on;transport=tcp;ue>
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uas
Require: 100rel,precondition,timer
RSeq: 1
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 524
P-Asserted-Identity: <tel:+15550000006>
P-Asserted-Identity: <sip:+15550000006@ims.mnc001.mcc001.3gppnetwork.org;user=phone>

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791147487 0 IN IP4 10.46.0.17
s=-
c=IN IP4 10.46.0.17
b=AS:42
b=RS:0
b=RR:0
t=0 0
m=audio 49120 RTP/AVP 106 96
b=AS:42
b=RS:0
b=RR:0
a=rtpmap:106 EVS/16000
a=fmtp:106 br=5.9-24.4; bw=nb-wb; max-red=0; ch-aw-recv=2
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=des:qos mandatory local sendrecv
a=curr:qos local none
a=des:qos mandatory remote sendrecv
a=curr:qos remote none
a=conf:qos remote sendrecv
