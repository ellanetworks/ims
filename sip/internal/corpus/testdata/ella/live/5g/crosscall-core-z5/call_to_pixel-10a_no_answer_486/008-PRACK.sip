PRACK sip:15550000002@10.46.0.3:49165 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKQGOEFHQ32HSVL6C2FKAS7BFX3M
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK3O4EGHAXYIAHWU5XDNMQZQGZ6K
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKDFWN4X6L7TXSAHEC53PCJMXE2J
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK2M56FQSIJT5IBB2V3UB7SQCMPX
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=103428407
To: <tel:15550000002;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=fb185c80
Call-ID: 103428400_1759955408@10.46.0.2
CSeq: 103428401 PRACK
Content-Length: 454
Via: SIP/2.0/TCP 10.46.0.2:43300;branch=z9hG4bK3515531254
Max-Forwards: 66
RAck: 1 103428400 INVITE
Content-Type: application/sdp
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Supported: timer
Require: precondition

v=0
o=- 14 1001 IN IP4 10.46.0.2
s=QC VOIP
c=IN IP4 10.46.0.2
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50032 RTP/AVP 104 96
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
