UPDATE sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKEMB23FVYUDYDFGRWT54C3Z6KCZ
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKTE22QYAWP3SDK6KOGMETNZTYKL
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKQO2NQZRO63P2MORZ6NPB3C5NHC
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK4C3NVDNOCTOFKIE2U3VT4TBAAP
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=394072
To: <tel:15550000004;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=394351
CSeq: 394067 UPDATE
Call-ID: 394065_1760685952@10.46.0.13
Content-Length: 455
Via: SIP/2.0/TCP 10.46.0.13:40279;branch=z9hG4bK4210244373
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=7GG5H3RMN2RF7OIZ;ue>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=7GG5H3RMN2RF7OIZ>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=7X6ORYJQHEJWZ5MV>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=OWJA5MIMJ24SYSKN>
Record-Route: <sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5060;lr;r2=on;did=GG3KIJ6ALIP7ALU3>
Record-Route: <sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5063;transport=tcp;lr;r2=on;did=GG3KIJ6ALIP7ALU3;ue>
Max-Forwards: 66
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Contact: <sip:6360b6ba-8a1f-493d-a05a-f4121465d17e@10.46.0.13:40279>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition

v=0
o=- 7 1001 IN IP4 10.46.0.13
s=QC VOIP
c=IN IP4 10.46.0.13
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50048 RTP/AVP 104 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
