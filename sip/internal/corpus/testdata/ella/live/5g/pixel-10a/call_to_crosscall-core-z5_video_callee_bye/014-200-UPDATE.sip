SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.46.0.7:49171;branch=z9hG4bKd00abb544602480663544e86
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=4adeb09b
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=112583285
Call-ID: 4f22af16a24cc6@10.46.0.7
CSeq: 3 UPDATE
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5063;transport=tcp;lr;r2=on;did=XRD7U2VCTPNTUUZG;ue>,<sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5060;lr;r2=on;did=XRD7U2VCTPNTUUZG>,<sip:mt@10.80.0.5:5080;lr;did=5ZTFK5NBWEG5AMWQ>,<sip:mo@10.80.0.5:5080;lr;did=R4U4EG3J7F7VKVMW>,<sip:t7kgpikxbisiecwrwzojjcbryu@10.80.0.5:5060;lr;r2=on;did=W6MQZ3OSVOAPPCMC>,<sip:t7kgpikxbisiecwrwzojjcbryu@10.80.0.5:5063;transport=tcp;lr;r2=on;did=W6MQZ3OSVOAPPCMC;ue>
Content-Length: 946
Content-Type: application/sdp
Session-ID: d22826c3284554e6a99141f3b92263d2
Server: Core-Z5 N1982U.6.02.09.FR00
Contact: <sip:2da86196-c8f7-4939-af8b-fbdac4e983f2@10.46.0.8:42225>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: timer,precondition
Supported: timer,100rel,from-change,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Session-Expires: 1800;refresher=uac

v=0
o=- 26 1001 IN IP4 10.46.0.8
s=QC VOIP
c=IN IP4 10.46.0.8
b=AS:425
b=RS:5800
b=RR:8000
t=0 0
m=audio 50032 RTP/AVP 100 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:100 AMR-WB/16000/1
a=fmtp:100 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
m=video 60010 RTP/AVPF 116
b=AS:384
b=RS:5200
b=RR:6000
a=rtpmap:116 H264/90000
a=fmtp:116 profile-level-id=42C00C;packetization-mode=1;sar-understood=16;sar-supported=1;sprop-parameter-sets=Z0LADNoPCmgG0KE1,aM4G4g==
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=rtcp-fb:* nack
a=rtcp-fb:* nack pli
a=rtcp-fb:* ccm fir
a=rtcp-fb:* ccm tmmbr
a=sendrecv
a=extmap:7 urn:3gpp:video-orientation
