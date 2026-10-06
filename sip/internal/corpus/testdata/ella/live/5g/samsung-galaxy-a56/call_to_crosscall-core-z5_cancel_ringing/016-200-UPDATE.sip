SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.46.0.7:6600;branch=z9hG4bK-524287-1---a4ca2fb718a6ffbf;rport=6601;transport=TCP;received=10.46.0.7
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=2BAVT3L3NFJ6FVKB;ue>,<sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5060;lr;r2=on;did=2BAVT3L3NFJ6FVKB>,<sip:mt@10.80.0.5:5080;lr;did=TDFZP5D4FBP6QJJR>,<sip:mo@10.80.0.5:5080;lr;did=VENQN7E3JGGY36BT>,<sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5060;lr;r2=on;did=IWPS44BH35RETB3G>,<sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5063;transport=tcp;lr;r2=on;did=IWPS44BH35RETB3G;ue>
To: <sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone>;tag=100319479
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=89465902
Call-ID: u_J0EuKkKtGg8_4krDbN1g..@10.46.0.7
CSeq: 3 UPDATE
Session-Expires: 1800;refresher=uac
Content-Type: application/sdp
Content-Length: 460
Server: Core-Z5 N1982U.6.02.09.FR00
Contact: <sip:cbcd92dc-db6f-4aea-a207-e26b95efb9f2@10.46.0.8:41989>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: timer,precondition
Supported: 100rel,from-change,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS

v=0
o=- 6 1001 IN IP4 10.46.0.8
s=QC VOIP
c=IN IP4 10.46.0.8
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50044 RTP/AVP 116 111
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=0
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
