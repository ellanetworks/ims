UPDATE sip:2da86196-c8f7-4939-af8b-fbdac4e983f2@10.46.0.8:42225 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKQYXEXESPP2CUNBJY3RWXU7W74P
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKEOPSDBO5YD4KN7E27SOL3PNST5
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKV3QWD53U2MI4QYIH4DKOS2FARH
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKGYECBOOP5JFA46QXM4ILCPNXVX
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=4adeb09b
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=112583285
Contact: <sip:15550000002@10.46.0.7:49171>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Call-ID: 4f22af16a24cc6@10.46.0.7
CSeq: 3 UPDATE
Max-Forwards: 66
Via: SIP/2.0/TCP 10.46.0.7:49171;branch=z9hG4bKd00abb544602480663544e86
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5063;transport=tcp;lr;r2=on;did=XRD7U2VCTPNTUUZG;ue>
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5060;lr;r2=on;did=XRD7U2VCTPNTUUZG>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=5ZTFK5NBWEG5AMWQ>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=R4U4EG3J7F7VKVMW>
Record-Route: <sip:t7kgpikxbisiecwrwzojjcbryu@10.80.0.5:5060;lr;r2=on;did=W6MQZ3OSVOAPPCMC>
Record-Route: <sip:t7kgpikxbisiecwrwzojjcbryu@10.80.0.5:5063;transport=tcp;lr;r2=on;did=W6MQZ3OSVOAPPCMC;ue>
Content-Length: 931
Content-Type: application/sdp
Supported: 100rel,199,histinfo,precondition,sec-agree,timer
Allow: INVITE,CANCEL,UPDATE,PRACK,BYE,ACK,MESSAGE,SUBSCRIBE,NOTIFY,PUBLISH,REFER,INFO,OPTIONS
User-Agent: Google_Pixel10a_Android 16_BD6A.251031.001.A4
Session-ID: d22826c3284554e6a99141f3b92263d2
Require: precondition

v=0
o=sip:IMS_RCS-USER 12345679 12345679 IN IP4 10.46.0.7
s=VIDEOCALL
c=IN IP4 10.46.0.7
b=AS:425
b=RS:5800
b=RR:8000
t=0 0
m=audio 7010 RTP/AVP 100 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:100 AMR-WB/16000/1
a=fmtp:100 mode-change-capability=2; max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
m=video 26300 RTP/AVPF 116
b=AS:384
b=RS:5200
b=RR:6000
a=rtpmap:116 H264/90000/1
a=fmtp:116 profile-level-id=42C00C; packetization-mode=1; sprop-parameter-sets=Z0LADNoPCmQ=,aM4G8g==
a=rtcp-fb:* nack
a=rtcp-fb:* nack pli
a=rtcp-fb:* ccm fir
a=rtcp-fb:* ccm tmmbr
a=extmap:7 urn:3gpp:video-orientation
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
