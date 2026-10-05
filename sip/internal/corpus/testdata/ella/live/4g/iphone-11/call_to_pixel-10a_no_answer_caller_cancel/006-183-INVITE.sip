SIP/2.0 183 Session Progress
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=N9VFxKYIi7
To: <tel:+15550000002>;tag=10f68f23
Contact: <sip:15550000002@10.46.0.24:49165>;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video
Call-ID: Vp5lvr2fZa3rj8x005nYYEPM
CSeq: 1 INVITE
Via: SIP/2.0/TCP 10.46.0.25:63507;received=10.46.0.25;branch=z9hG4bKpJ8Du7v3SmDc1Cp;rport=64105
Require: 100rel,precondition
Content-Length: 472
Supported: 100rel,precondition,timer
Allow: INVITE,CANCEL,UPDATE,PRACK,BYE,ACK,MESSAGE,SUBSCRIBE,NOTIFY,PUBLISH,REFER,INFO,OPTIONS
Content-Type: application/sdp
Server: Google_Pixel10a_Android 16_BD6A.251031.001.A4
Record-Route: <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5063;transport=tcp;lr;r2=on;ob;did=5FM5CGBUFV4OODSU;ue>,<sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5060;lr;r2=on;ob;did=5FM5CGBUFV4OODSU>,<sip:mt@10.80.0.5:5080;lr;did=NW5S4KKIPFQWPQUA>,<sip:mo@10.80.0.5:5080;lr;did=KACYVEHWOAQWQUXS>,<sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5060;lr;r2=on;did=HWFAUINCWZVHSK4D>,<sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5063;transport=tcp;lr;r2=on;did=HWFAUINCWZVHSK4D;ue>
RSeq: 1
Session-ID: 81ea83e819dd815b4cc760f421299969
P-Asserted-Identity: <tel:+15550000002>
P-Asserted-Identity: <sip:+15550000002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>

v=0
o=sip:IMS_RCS-USER 12345682 12345679 IN IP4 10.46.0.24
s=VOLTECALL
c=IN IP4 10.46.0.24
t=0 0
m=audio 7010 RTP/AVP 116 105
b=AS:30
b=RS:512
b=RR:1537
a=rtpmap:116 EVS/16000/1
a=fmtp:116 br=13.2; bw=nb-wb; max-red=220
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=ptime:20
a=maxptime:240
a=inactive
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=conf:qos remote sendrecv
