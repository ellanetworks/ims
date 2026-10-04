INVITE sip:10.46.0.17:61189 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKFR7RT5XSHS6GFLSXFINNNUJLB2
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKBR5X2K7QHZW6G3ZIATTUYS7D46
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKUQSETJKE7RJM3RR7SUTY7SSGUN
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKNILFBP4UE7DG53CENYPPQTNLRK
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKFNTMA2G4X5OJ7JYMEATKQXJNE4
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=2285999
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
CSeq: 2285993 INVITE
Call-ID: 2285993_3069112144@10.46.0.15
Via: SIP/2.0/TCP 10.46.0.15:43435;branch=z9hG4bK815309176
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;transport=tcp;lr;r2=on;did=OZ5L5WPXEWEZT5DE;ue>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;lr;r2=on;did=OZ5L5WPXEWEZT5DE>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=WDR4TWYSLKD4FGVV>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=GBN5MJRLAQ7OKFUP>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=D3QLDSGEZTLKUJJE>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=D3QLDSGEZTLKUJJE;ue>
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
o=- 23 1000 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50028 RTP/AVP 104 110 102 108 96 97
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
