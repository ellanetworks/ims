SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.46.0.13:40279;branch=z9hG4bK1584349268
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=354254
To: <tel:15550000004;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=354707
CSeq: 354252 UPDATE
Call-ID: 354250_1760822368@10.46.0.13
Content-Length: 459
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=LH2F52M7MD4EXCGO;ue>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=LH2F52M7MD4EXCGO>,<sip:mt@10.80.0.5:5080;lr;did=UQC6CAJAE5RADFSU>,<sip:mo@10.80.0.5:5080;lr;did=ZORNBAWCUPERX3S5>,<sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5060;lr;r2=on;did=OC466DP3NUUY5YWK>,<sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5063;transport=tcp;lr;r2=on;did=OC466DP3NUUY5YWK;ue>
Content-Type: application/sdp
Server: Core-Z5 N1982U.6.02.09.FR00
Contact: <sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition
Supported: timer,100rel,from-change,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Session-Expires: 1800;refresher=uas

v=0
o=- 9 1001 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50042 RTP/AVP 104 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
