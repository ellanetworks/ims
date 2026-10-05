UPDATE sip:15550000002@10.46.0.24:49165 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK4JPDHWE3L2REHCENXI67I3SHE2
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKBUIHN26LXEH4GT6IXJOJNHYBQ2
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKLS6WEPRPRXDBFVSWRGKIVABZUE
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKFVHYGUNV27YAWJBZ3ZTPVSG5FF
Supported: 100rel,path,replaces,timer
To: <tel:+15550000002>;tag=ff17475
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=lS8LckkC5a
Call-ID: lfL6PNmhoTLxV8066vuy5N6Z
Session-ID: 3409143487ce4c36db1c6754249bde48
Contact: <sip:10.46.0.25:63507>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
CSeq: 3 UPDATE
Via: SIP/2.0/TCP 10.46.0.25:63507;branch=z9hG4bKrZJRg6dp1tpyqft;rport=64105;received=10.46.0.25
Record-Route: <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5063;did=7CIA77CNYADYJMIR;lr;ob;r2=on;transport=tcp;ue>
Record-Route: <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5060;did=7CIA77CNYADYJMIR;lr;ob;r2=on>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=OFE4UUYDMOQSPUN4>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=HEVSEEELGHF4VVIO>
Record-Route: <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5060;did=PT6TEW2SCZHAUG5T;lr;r2=on>
Record-Route: <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5063;did=PT6TEW2SCZHAUG5T;lr;r2=on;transport=tcp;ue>
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 66
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 496
Require: precondition

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791202069 1791202070 IN IP4 10.46.0.25
s=-
c=IN IP4 10.46.0.25
b=AS:30
b=RS:0
b=RR:0
t=0 0
m=audio 49120 RTP/AVP 116 105
b=AS:30
b=RS:0
b=RR:0
a=rtpmap:116 EVS/16000
a=fmtp:116 br=13.2; bw=nb-wb; max-red=220
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=des:qos mandatory local sendrecv
a=curr:qos local sendrecv
a=des:qos mandatory remote sendrecv
a=curr:qos remote none
