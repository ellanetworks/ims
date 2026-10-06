SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.46.0.8:41989;branch=z9hG4bK1104116071
Max-Forwards: 70
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5063;transport=tcp;lr;r2=on;did=4NKCGDJOCMC2HPOJ;ue>
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5060;lr;r2=on;did=4NKCGDJOCMC2HPOJ>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=OCQD6PWAEM3MXY4W>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=E6U2IYYL6IBKRX6G>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5060;lr;r2=on;did=O4LDYHSICKSZYIHD>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=O4LDYHSICKSZYIHD;ue>
Require: precondition
Contact: <sip:15550000007@10.46.0.7:6600>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <tel:15550000007;phone-context=ims.mnc001.mcc999.3gppnetwork.org>;tag=c9048402
From: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=100513383
Call-ID: 100513376_3069421008@10.46.0.8
CSeq: 100513378 UPDATE
Session-Expires: 1800;refresher=uas
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
Content-Length: 447

v=0
o=SAMSUNG-IMS-UE 186363504175 1 IN IP4 10.46.0.7
s=SS VOIP
c=IN IP4 10.46.0.7
t=0 0
m=audio 1250 RTP/AVP 104 96
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
