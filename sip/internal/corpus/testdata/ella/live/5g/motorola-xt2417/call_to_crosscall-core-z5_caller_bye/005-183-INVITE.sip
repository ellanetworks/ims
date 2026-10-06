SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKN6XXWOCHRMRMRLUEU5ZU6CTAL4,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKUFPSJ5CCYKXG5KGDEKIKLVJXBD,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKQLPACDM6EEO3IMJDYKIXXN6CL2,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKQ6RZERGJO3FKY64P3D645CDWAW,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKZNOF3MWU6EUWL6ZCH7T2UAJ24L,SIP/2.0/TCP 10.46.0.9:43885;branch=z9hG4bK3439066730
From: <sip:15550000008@ims.mnc001.mcc001.3gppnetwork.org>;tag=110381187
To: <sip:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=110381076
CSeq: 110381182 INVITE
Call-ID: 110381182_4109868208@10.46.0.9
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5063;transport=tcp;lr;r2=on;did=5AX5VLJIYKXJI5TD;ue>,<sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5060;lr;r2=on;did=5AX5VLJIYKXJI5TD>,<sip:mt@10.80.0.5:5080;lr;did=UBVOTASGC3S2Q5JR>,<sip:mo@10.80.0.5:5080;lr;did=S3IWEBVY7OHMTTVY>,<sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5060;lr;r2=on;did=7D5MQKY37NSIRTAU>,<sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5063;transport=tcp;lr;r2=on;did=7D5MQKY37NSIRTAU;ue>
Content-Type: application/sdp
Content-Length: 475
Server: Core-Z5 N1982U.6.02.09.FR00
RSeq: 1
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=0010100000100066C000
Contact: <sip:2da86196-c8f7-4939-af8b-fbdac4e983f2@10.46.0.8:42225>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: 100rel,precondition
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS

v=0
o=- 15 1000 IN IP4 10.46.0.8
s=QC VOIP
c=IN IP4 10.46.0.8
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50016 RTP/AVP 99 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:99 AMR-WB/16000/1
a=fmtp:99 mode-change-capability=2;max-red=0
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
