PRACK sip:a87a2e9a-4c32-439f-afc0-825ea2f8b236@10.46.0.9:43885 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKZX5NUWLVBWRYECS5PUQDLOAYRW
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKD4AQBKXDRBXR3UODMN37EQS6TO
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK5XZ7XHZ6JW43IZEYSAVDQNDAGR
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKV6KHI4DFHSHEDLBT5FLGK6EDEV
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=111319409
To: <tel:15550000008;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=111319698
CSeq: 111319403 PRACK
Call-ID: 111319402_1761473040@10.46.0.8
Content-Length: 471
Via: SIP/2.0/TCP 10.46.0.8:42225;branch=z9hG4bK3470341005
Max-Forwards: 66
RAck: 1 111319402 INVITE
Content-Type: application/sdp
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Supported: timer
Require: precondition

v=0
o=- 21 1001 IN IP4 10.46.0.8
s=QC VOIP
c=IN IP4 10.46.0.8
b=AS:37
b=RS:600
b=RR:2000
t=0 0
m=audio 50054 RTP/AVP 104 96
b=AS:37
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-set=3,4,5,6;mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
