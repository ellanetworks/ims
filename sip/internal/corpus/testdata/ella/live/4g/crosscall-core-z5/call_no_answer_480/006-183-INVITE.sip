SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.46.0.13:40279;branch=z9hG4bK2233954828
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=394072
To: <tel:15550000004;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=394351
CSeq: 394065 INVITE
Call-ID: 394065_1760685952@10.46.0.13
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=7GG5H3RMN2RF7OIZ;ue>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=7GG5H3RMN2RF7OIZ>,<sip:mt@10.80.0.5:5080;lr;did=7X6ORYJQHEJWZ5MV>,<sip:mo@10.80.0.5:5080;lr;did=OWJA5MIMJ24SYSKN>,<sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5060;lr;r2=on;did=GG3KIJ6ALIP7ALU3>,<sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5063;transport=tcp;lr;r2=on;did=GG3KIJ6ALIP7ALU3;ue>
Content-Type: application/sdp
Content-Length: 480
Server: Core-Z5 N1982U.6.02.09.FR00
RSeq: 1
Contact: <sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: 100rel,precondition
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
P-Asserted-Identity: <tel:+15550000004>
P-Asserted-Identity: <sip:+15550000004@ims.mnc001.mcc001.3gppnetwork.org;user=phone>

v=0
o=- 10 1000 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50030 RTP/AVP 104 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=conf:qos remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
