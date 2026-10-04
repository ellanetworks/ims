INVITE sip:10.46.0.17:61189 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKWWKMCWFGGLCZGTWIPIRO7BQF6Y
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKXRNVHG77G2J6RSX5ZXBECMLV44
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKUHQJRXOPZMU4MCKM2QP7TJTWQ5
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKFIKAJFHY5YZWMHEJ74NZVURF5C
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKMQFACNLYNZPBACCNDQHCZNCZ4M
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=2709695
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
CSeq: 2709688 INVITE
Call-ID: 2709688_3069114096@10.46.0.15
Via: SIP/2.0/TCP 10.46.0.15:43435;branch=z9hG4bK2540163860
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;transport=tcp;lr;r2=on;did=WPUGTEUSZUEI3WKZ;ue>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;lr;r2=on;did=WPUGTEUSZUEI3WKZ>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=WYR7FTS4PGQYBJFH>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=JYUGX4TC663PQD5G>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=2IGOGQRNOJOD6PWC>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=2IGOGQRNOJOD6PWC;ue>
Max-Forwards: 65
Contact: <sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Supported: timer,199,100rel,replaces,precondition,from-change,histinfo,tdialog
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Length: 764
Session-Expires: 1800;refresher=uac
P-Called-Party-ID: <tel:+15550000006>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000004>

v=0
o=- 26 1000 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50050 RTP/AVP 104 110 102 108 96 97
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
