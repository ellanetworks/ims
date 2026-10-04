INVITE sip:10.46.0.17:61189 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKWUYEU5YSNXBYKNZ5WNM3W5KB7U
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKO6XTRE6UFKPZUYQSI6RONQZILA
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK543ST254DOO4WEUSBRKYNJNIJI
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKQHKRSFTSTTFNPUU5YRV3BRHYTZ
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKDZ37U3T456Z6MVWT5YCFHCLX4U
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=2338394
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
CSeq: 2338387 INVITE
Call-ID: 2338387_3069112144@10.46.0.15
Via: SIP/2.0/TCP 10.46.0.15:43435;branch=z9hG4bK833306649
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;transport=tcp;lr;r2=on;did=UA6GW5KBATBM63DW;ue>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;lr;r2=on;did=UA6GW5KBATBM63DW>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=PB5JDLZ373UDY2NT>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=KEAATBOI7DZVIHM3>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=5KMZ6TICTZ77OVZG>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=5KMZ6TICTZ77OVZG;ue>
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
o=- 24 1000 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
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
