SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.46.0.12:40079;branch=z9hG4bK2279200514
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=10999539
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=10999940
CSeq: 10999534 INVITE
Call-ID: 10999534_3068109552@10.46.0.12
Record-Route: <sip:vi2bwmwnrzaymflx5aiknm7xty@10.80.0.5:5063;transport=tcp;lr;r2=on;did=PS2WY5JAQIREWCJM;ue>,<sip:vi2bwmwnrzaymflx5aiknm7xty@10.80.0.5:5060;lr;r2=on;did=PS2WY5JAQIREWCJM>,<sip:mt@10.80.0.5:5080;lr;did=2OXHFD2XSKML5XQY>,<sip:mo@10.80.0.5:5080;lr;did=E7Z55NQRC655NCXL>,<sip:w35g4hq4urdemhjo35tydi7tnf@10.80.0.5:5060;lr;r2=on;did=CMK44DQVMHKNCUTP>,<sip:w35g4hq4urdemhjo35tydi7tnf@10.80.0.5:5063;transport=tcp;lr;r2=on;did=CMK44DQVMHKNCUTP;ue>
Content-Type: application/sdp
Content-Length: 479
Server: Core-Z5 N1982U.6.02.09.FR00
RSeq: 1
Contact: <sip:6f0ab00c-bded-407e-a8a3-721fc172104b@10.46.0.11:41290>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: 100rel,precondition
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
P-Asserted-Identity: <tel:+15550000005>
P-Asserted-Identity: <sip:+15550000005@ims.mnc001.mcc001.3gppnetwork.org;user=phone>

v=0
o=- 3 1000 IN IP4 10.46.0.11
s=QC VOIP
c=IN IP4 10.46.0.11
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50016 RTP/AVP 104 96
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
