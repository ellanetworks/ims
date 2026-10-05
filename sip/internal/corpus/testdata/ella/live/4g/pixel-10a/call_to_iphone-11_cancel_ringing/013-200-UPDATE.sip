SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.46.0.24:49165;branch=z9hG4bKc20d5bad30610c21a0b6071b
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=c93814d0
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=gZHTxRMICy
Call-ID: 69b05bfa9481269b@10.46.0.24
CSeq: 3 UPDATE
Record-Route: <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5063;did=G7NW3CV263564C77;lr;r2=on;transport=tcp;ue>, <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5060;did=G7NW3CV263564C77;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=GE2MWKHJ675X3E6S;lr>, <sip:mo@10.80.0.5:5080;did=NUVZHBQMUKZQHLUU;lr>, <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5060;did=KLBKX475MTBLA2XK;lr;r2=on>, <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5063;did=KLBKX475MTBLA2XK;lr;r2=on;transport=tcp;ue>
Session-ID: 38c5cf5883ac542794dc32dc9e2067c0
Require: precondition,timer
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uac
Contact: <sip:10.46.0.25:63507>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 514

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791202871 1 IN IP4 10.46.0.25
s=-
c=IN IP4 10.46.0.25
b=AS:42
b=RS:0
b=RR:0
t=0 0
m=audio 49120 RTP/AVP 127 96
b=AS:42
b=RS:0
b=RR:0
a=rtpmap:127 EVS/16000
a=fmtp:127 br=5.9-24.4; bw=nb-wb; max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=des:qos mandatory local sendrecv
a=curr:qos local none
a=des:qos mandatory remote sendrecv
a=curr:qos remote sendrecv
a=conf:qos remote sendrecv
