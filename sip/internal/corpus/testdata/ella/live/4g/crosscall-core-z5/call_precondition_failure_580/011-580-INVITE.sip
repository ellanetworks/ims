SIP/2.0 580 Precondition Failure
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK7PXNDA4P5QIUNSCX762DYWW7SL,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKZQ5GXE5JSXMCTUOUOR5UKXHTKC,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK3JDOV7RFQYGYSCIRDDOM7CCFHE,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKDUHLXHXDX7WZKVURPQKXPT6NXF,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKMOXCGOTVK5YHLNJ5DEEWI56XEY,SIP/2.0/TCP 10.46.0.10:44649;branch=z9hG4bK781178675
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=2769108
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=2769268
CSeq: 2769104 INVITE
Call-ID: 2769104_3070651744@10.46.0.10
Content-Type: application/sdp
Content-Length: 443
Server: Core-Z5 N1982U.6.02.09.FR00
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015EE0000
Reason: RELEASE_CAUSE ;cause=3 ;text="Media bearer loss"
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS

v=0
o=- 2 1001 IN IP4 10.46.0.9
s=QC VOIP
c=IN IP4 10.46.0.9
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
