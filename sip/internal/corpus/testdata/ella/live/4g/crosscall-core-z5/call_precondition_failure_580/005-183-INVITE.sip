SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK7PXNDA4P5QIUNSCX762DYWW7SL,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKZQ5GXE5JSXMCTUOUOR5UKXHTKC,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK3JDOV7RFQYGYSCIRDDOM7CCFHE,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKDUHLXHXDX7WZKVURPQKXPT6NXF,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKMOXCGOTVK5YHLNJ5DEEWI56XEY,SIP/2.0/TCP 10.46.0.10:44649;branch=z9hG4bK781178675
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=2769108
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=2769268
CSeq: 2769104 INVITE
Call-ID: 2769104_3070651744@10.46.0.10
Record-Route: <sip:6i4q2m6nxni36zdy7tnec54vkr@10.80.0.5:5063;transport=tcp;lr;r2=on;did=I5EP33QKNPR3P446;ue>,<sip:6i4q2m6nxni36zdy7tnec54vkr@10.80.0.5:5060;lr;r2=on;did=I5EP33QKNPR3P446>,<sip:mt@10.80.0.5:5080;lr;did=AEPJBRYSO4UHHE77>,<sip:mo@10.80.0.5:5080;lr;did=JOGXN4UVTJ5MLE77>,<sip:wun37scwecanwhwdbxcym333os@10.80.0.5:5060;lr;r2=on;did=ZRD2XWQJED43TL2H>,<sip:wun37scwecanwhwdbxcym333os@10.80.0.5:5063;transport=tcp;lr;r2=on;did=ZRD2XWQJED43TL2H;ue>
Content-Type: application/sdp
Content-Length: 477
Server: Core-Z5 N1982U.6.02.09.FR00
RSeq: 1
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015EE0000
Contact: <sip:c8467f99-2d1f-47fd-a899-8956c40881a3@10.46.0.9:44090>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: 100rel,precondition
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS

v=0
o=- 2 1000 IN IP4 10.46.0.9
s=QC VOIP
c=IN IP4 10.46.0.9
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50040 RTP/AVP 104 96
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
