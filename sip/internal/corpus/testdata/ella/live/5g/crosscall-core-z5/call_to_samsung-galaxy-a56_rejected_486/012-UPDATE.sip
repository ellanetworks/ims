UPDATE sip:15550000007@10.46.0.7:6600 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKPNCUCSUHE7HRHVUVTI7BK5IYVB
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKP4KSKY5OGVXIAZ6ZOHGFERYOYJ
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKI33IQJSDY3A47CFTBKNMMAGTLT
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKJFMJQQQTUKL75N3XPJ5S4CNDTD
Max-Forwards: 66
To: <tel:15550000007;phone-context=ims.mnc001.mcc999.3gppnetwork.org>;tag=23426e4a
From: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=100699893
Call-ID: 100699886_3068622352@10.46.0.8
CSeq: 100699888 UPDATE
Content-Length: 453
Via: SIP/2.0/TCP 10.46.0.8:41989;branch=z9hG4bK2102119903
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5063;transport=tcp;lr;r2=on;did=FLUIUSQENRDFGD2K;ue>
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5060;lr;r2=on;did=FLUIUSQENRDFGD2K>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=Q6G6GKCZOK2FLXON>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=EVNTE6S5UQASFCPT>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5060;lr;r2=on;did=OFUBLLFGBYHHRK5W>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=OFUBLLFGBYHHRK5W;ue>
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Contact: <sip:cbcd92dc-db6f-4aea-a207-e26b95efb9f2@10.46.0.8:41989>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition

v=0
o=- 8 1001 IN IP4 10.46.0.8
s=QC VOIP
c=IN IP4 10.46.0.8
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50052 RTP/AVP 104 96
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
