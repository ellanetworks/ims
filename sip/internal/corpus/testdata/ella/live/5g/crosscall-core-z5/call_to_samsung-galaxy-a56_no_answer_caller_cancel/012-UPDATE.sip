UPDATE sip:15550000007@10.46.0.7:6600 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKJW7YE6ZOEVDMNRGJU67DA5NNFT
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK3GMLRSM2GXMA2FWPNJC2JWFFLR
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKCCRTLS3WFH75RSURUXHEZQI2DK
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKN2SS6CS65LIXPXK7SGD753IZBO
Max-Forwards: 66
To: <tel:15550000007;phone-context=ims.mnc001.mcc999.3gppnetwork.org>;tag=182a5d7a
From: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=100791154
Call-ID: 100791148_3068993808@10.46.0.8
CSeq: 100791149 UPDATE
Content-Length: 453
Via: SIP/2.0/TCP 10.46.0.8:41989;branch=z9hG4bK1556401713
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5063;transport=tcp;lr;r2=on;did=M5SM2HEFRGAHI36A;ue>
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5060;lr;r2=on;did=M5SM2HEFRGAHI36A>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=Z4QDMBAGCXEEIS3F>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=WXYOJWTZ4LBBKHVN>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5060;lr;r2=on;did=752RRKDPKX56IKFN>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=752RRKDPKX56IKFN;ue>
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Contact: <sip:cbcd92dc-db6f-4aea-a207-e26b95efb9f2@10.46.0.8:41989>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition

v=0
o=- 9 1001 IN IP4 10.46.0.8
s=QC VOIP
c=IN IP4 10.46.0.8
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
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
