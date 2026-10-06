SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKNOL33W5O7G6VMOX7RNRHZT2NWB,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK2YVZUI3ULKB5E3QENPNMAZIZZY,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKR7KBQNXFEN4PEB43VQPVG24BB7,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKHGCTWXQ5AJBPYOFWF263KGU2DX,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKCPQZFQH7NVFAJX3MNXQG6PK5OA,SIP/2.0/TCP 10.46.0.5:6500;branch=z9hG4bK-524287-1---676b95983374862b;rport=6501;transport=TCP;received=10.46.0.5
Record-Route: <sip:a4jyxqxxrkgzk5gcxwc37cj5mu@10.80.0.5:5063;transport=tcp;lr;r2=on;did=YKQTOSJSBY5MM6SY;ue>,<sip:a4jyxqxxrkgzk5gcxwc37cj5mu@10.80.0.5:5060;lr;r2=on;did=YKQTOSJSBY5MM6SY>,<sip:mt@10.80.0.5:5080;lr;did=ECKPY734NSIBAK5Q>,<sip:mo@10.80.0.5:5080;lr;did=YLRXN2OJ2SHUOG4S>,<sip:yuvey7vgymc2aajptku4giz3c7@10.80.0.5:5060;lr;r2=on;did=BGW57IAEJAAWUT2W>,<sip:yuvey7vgymc2aajptku4giz3c7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=BGW57IAEJAAWUT2W;ue>
To: <sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone>;tag=99052249
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=551cef53
Call-ID: cq2bdNGeGUuZz-QZvV6utw..@10.46.0.5
CSeq: 1 INVITE
Content-Type: application/sdp
Content-Length: 476
Server: Core-Z5 N1982U.6.02.09.FR00
RSeq: 1
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=9990100000100066C000
Contact: <sip:3b7d83d2-0108-4629-a2dd-0f4c6e59a52d@10.46.0.6:40117>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: 100rel,precondition
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS

v=0
o=- 2 1000 IN IP4 10.46.0.6
s=QC VOIP
c=IN IP4 10.46.0.6
b=AS:41
b=RS:0
b=RR:2500
t=0 0
m=audio 50038 RTP/AVP 116 111
b=AS:41
b=RS:0
b=RR:2500
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=0
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=conf:qos remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
