INVITE sip:cbcd92dc-db6f-4aea-a207-e26b95efb9f2@10.46.0.8:41989 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK6QY6OTGJDIDHFROPTAPDO4XBJN
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKRWFKTLSNQ7QBYZP77YWHF6RLXT
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK53KGK6H4ACMWENARHFXT2ILCQH
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKYQ2C44EV3F4NPX45WVQZNAU63G
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK5IZQ5CCV3WMWE2T4ZTGJKJMB76
Via: SIP/2.0/TCP 10.46.0.7:6600;branch=z9hG4bK-524287-1---ea79a4bc285f365c;rport=6601;transport=TCP;received=10.46.0.7
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=2BAVT3L3NFJ6FVKB;ue>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5060;lr;r2=on;did=2BAVT3L3NFJ6FVKB>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=TDFZP5D4FBP6QJJR>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=VENQN7E3JGGY36BT>
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5060;lr;r2=on;did=IWPS44BH35RETB3G>
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5063;transport=tcp;lr;r2=on;did=IWPS44BH35RETB3G;ue>
Max-Forwards: 65
Contact: <sip:15550000007@10.46.0.7:6600>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone>
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=89465902
Call-ID: u_J0EuKkKtGg8_4krDbN1g..@10.46.0.7
CSeq: 1 INVITE
Session-Expires: 1800
Accept: application/sdp, application/3gpp-ims+xml
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition, gruu, sec-agree
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Content-Length: 870
P-Called-Party-ID: <tel:+15550000008>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000007>

v=0
o=SAMSUNG-IMS-UE 186169347617 0 IN IP4 10.46.0.7
s=SS VOIP
c=IN IP4 10.46.0.7
t=0 0
m=audio 1248 RTP/AVP 112 116 107 118 96 111 110
b=AS:42
b=RS:0
b=RR:2500
a=rtpmap:112 EVS/16000
a=fmtp:112 br=5.9-24.4;bw=nb-swb;ch-aw-recv=-1
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=220
a=rtpmap:107 AMR-WB/16000/1
a=fmtp:107 octet-align=1;mode-change-capability=2;max-red=220
a=rtpmap:118 AMR/8000/1
a=fmtp:118 mode-set=0,2,4,7;mode-change-capability=2;max-red=220
a=rtpmap:96 AMR/8000/1
a=fmtp:96 mode-set=0,2,4,7;octet-align=1;mode-change-capability=2;max-red=220
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=rtpmap:110 telephone-event/8000
a=fmtp:110 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
a=sendrecv
a=ptime:20
a=maxptime:240
