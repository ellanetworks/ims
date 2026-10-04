CANCEL tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org SIP/2.0
Content-Length: 445
Content-Type: application/sdp
Reason: RELEASE_CAUSE ;cause=3 ;text="Media bearer loss"
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=2769108
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
Call-ID: 2769104_3070651744@10.46.0.10
Max-Forwards: 70
CSeq: 2769104 CANCEL
Via: SIP/2.0/TCP 10.46.0.10:44649;branch=z9hG4bK781178675
Route: <sip:10.80.0.5:5063;lr>,<sip:orig-6@scscf.ims.mnc001.mcc001.3gppnetwork.org:5080;lr>
User-Agent: Core-Z5 N1982U.6.02.09.FR00

v=0
o=- 5 1001 IN IP4 10.46.0.10
s=QC VOIP
c=IN IP4 10.46.0.10
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 0 RTP/AVP 104 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos failure local sendrecv
a=des:qos mandatory remote sendrecv
a=inactive
a=maxptime:240
a=ptime:20
