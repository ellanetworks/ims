SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.46.0.15:43435;branch=z9hG4bK1587578122
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=1010352
To: <tel:15550000001;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=1010648
CSeq: 1010345 PRACK
Call-ID: 1010344_3070881744@10.46.0.15
Content-Length: 472
Content-Type: application/sdp
Server: PRD-IR92/12 term-Motorola/XT2417-1-V1UFN35H.193-20 device-type/smart-phone
Require: precondition
Supported: 100rel,from-change,tdialog
Privacy: id
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS

v=0
o=- 6 1001 IN IP4 10.46.0.14
s=QC VOIP
c=IN IP4 10.46.0.14
b=AS:37
b=RS:600
b=RR:2000
t=0 0
m=audio 50032 RTP/AVP 104 96
b=AS:37
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-set=3,4,5,6;mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local none
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
