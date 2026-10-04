UPDATE sip:6f0ab00c-bded-407e-a8a3-721fc172104b@10.46.0.11:41290 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKE7DLHQGXJHV74DYV4DB2M6SQEE
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK54HXDDE55GJQVFJFCTD33CWIP7
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKKLD6QJOXMBCYY2XXAFH3ZGE67H
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKNE24UTQJG7JIKB2QFOQRQBHQ7S
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=10999539
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=10999940
CSeq: 10999536 UPDATE
Call-ID: 10999534_3068109552@10.46.0.12
Content-Length: 455
Via: SIP/2.0/TCP 10.46.0.12:40079;branch=z9hG4bK1605641076
Record-Route: <sip:vi2bwmwnrzaymflx5aiknm7xty@10.80.0.5:5063;transport=tcp;lr;r2=on;did=PS2WY5JAQIREWCJM;ue>
Record-Route: <sip:vi2bwmwnrzaymflx5aiknm7xty@10.80.0.5:5060;lr;r2=on;did=PS2WY5JAQIREWCJM>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=2OXHFD2XSKML5XQY>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=E7Z55NQRC655NCXL>
Record-Route: <sip:w35g4hq4urdemhjo35tydi7tnf@10.80.0.5:5060;lr;r2=on;did=CMK44DQVMHKNCUTP>
Record-Route: <sip:w35g4hq4urdemhjo35tydi7tnf@10.80.0.5:5063;transport=tcp;lr;r2=on;did=CMK44DQVMHKNCUTP;ue>
Max-Forwards: 66
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Contact: <sip:e17adb96-9fc3-4cf0-a7aa-db9e240fc2f3@10.46.0.12:40079>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition

v=0
o=- 6 1001 IN IP4 10.46.0.12
s=QC VOIP
c=IN IP4 10.46.0.12
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50060 RTP/AVP 104 96
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
