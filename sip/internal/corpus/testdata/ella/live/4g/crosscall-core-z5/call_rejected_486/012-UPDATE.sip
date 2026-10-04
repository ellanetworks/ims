UPDATE sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKCJBXXRRVHONHNLRPU5S4QLGMWG
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKVWCCSIU6VWTQUOPOAQTB45FENL
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKKJ46WLED6HYVHU2HFBN2XHIPAB
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKGMPK6SWPRYG7J2CRPUMZLNIVXI
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=263747
To: <tel:15550000004;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=264142
CSeq: 263741 UPDATE
Call-ID: 263739_1760018656@10.46.0.13
Content-Length: 455
Via: SIP/2.0/TCP 10.46.0.13:40279;branch=z9hG4bK1305549980
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=QEKL32EPKUOGABT6;ue>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=QEKL32EPKUOGABT6>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=T7CQDBYIE5L6JSN5>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=NZYATJZMBQ5NAF66>
Record-Route: <sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5060;lr;r2=on;did=QSWJH7JYD4HQGDRM>
Record-Route: <sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5063;transport=tcp;lr;r2=on;did=QSWJH7JYD4HQGDRM;ue>
Max-Forwards: 66
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Contact: <sip:6360b6ba-8a1f-493d-a05a-f4121465d17e@10.46.0.13:40279>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition

v=0
o=- 5 1001 IN IP4 10.46.0.13
s=QC VOIP
c=IN IP4 10.46.0.13
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50042 RTP/AVP 104 96
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
