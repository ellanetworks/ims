SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK3KP5RN2SRLI53SDBVYH4HZJKPB,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKSHAUYWWEN7VD3TFOORZQ37SKQ6,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKLMERKETLSQNE3G6S6WEGX5AUCA,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKASKHRNS2RG3KGCUXMBLVG24T5W,SIP/2.0/TCP 10.46.0.9:43885;branch=z9hG4bK1677449399
From: <sip:15550000008@ims.mnc001.mcc001.3gppnetwork.org>;tag=110881617
To: <sip:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=110881490
CSeq: 110881614 UPDATE
Call-ID: 110881612_4111788112@10.46.0.9
Content-Length: 455
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5063;transport=tcp;lr;r2=on;did=WUR2OLN5TUSFQHWY;ue>,<sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5060;lr;r2=on;did=WUR2OLN5TUSFQHWY>,<sip:mt@10.80.0.5:5080;lr;did=NDUEXWZS25XSJD53>,<sip:mo@10.80.0.5:5080;lr;did=O3AAO6XFWNFQPMHB>,<sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5060;lr;r2=on;did=YG2G7HU2UDU4LRHR>,<sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5063;transport=tcp;lr;r2=on;did=YG2G7HU2UDU4LRHR;ue>
Content-Type: application/sdp
Server: Core-Z5 N1982U.6.02.09.FR00
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=0010100000100066C000
Contact: <sip:2da86196-c8f7-4939-af8b-fbdac4e983f2@10.46.0.8:42225>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition
Supported: timer,100rel,from-change,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Session-Expires: 1800;refresher=uas

v=0
o=- 17 1001 IN IP4 10.46.0.8
s=QC VOIP
c=IN IP4 10.46.0.8
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50050 RTP/AVP 99 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:99 AMR-WB/16000/1
a=fmtp:99 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
