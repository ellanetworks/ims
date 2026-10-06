INVITE sip:2da86196-c8f7-4939-af8b-fbdac4e983f2@10.46.0.8:42225 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKY66BCHX7HBI73HF6C4CFKPTT2U
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKEUEHO57XFHZMLV2G3RODYFBBIV
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK6Y5EMAPKRN2UOHDM2UF4YTSQXS
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKQ7FF5SYK5D7YI3BEXHICNINVRE
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKWX6D3VA657VE6HNEXIKAM4TKWQ
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=4adeb09b
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
Contact: <sip:15550000002@10.46.0.7:49171>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Call-ID: 4f22af16a24cc6@10.46.0.7
CSeq: 1 INVITE
Max-Forwards: 65
Via: SIP/2.0/TCP 10.46.0.7:49171;branch=z9hG4bK792f55e2971d4a685376e1e4
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5063;transport=tcp;lr;r2=on;did=XRD7U2VCTPNTUUZG;ue>
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5060;lr;r2=on;did=XRD7U2VCTPNTUUZG>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=5ZTFK5NBWEG5AMWQ>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=R4U4EG3J7F7VKVMW>
Record-Route: <sip:t7kgpikxbisiecwrwzojjcbryu@10.80.0.5:5060;lr;r2=on;did=W6MQZ3OSVOAPPCMC>
Record-Route: <sip:t7kgpikxbisiecwrwzojjcbryu@10.80.0.5:5063;transport=tcp;lr;r2=on;did=W6MQZ3OSVOAPPCMC;ue>
Content-Length: 1762
Session-Expires: 1800;refresher=uac
Min-SE: 90
Allow: INVITE,CANCEL,UPDATE,PRACK,BYE,ACK,MESSAGE,SUBSCRIBE,NOTIFY,PUBLISH,REFER,INFO,OPTIONS
Content-Type: application/sdp
User-Agent: Google_Pixel10a_Android 16_BD6A.251031.001.A4
Supported: 100rel,199,histinfo,precondition,sec-agree,timer
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";video
Session-ID: d22826c3284554e6a99141f3b92263d2
Accept: application/sdp,application/3gpp-ims+xml
P-Called-Party-ID: <tel:+15550000005>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000002>

v=0
o=sip:IMS_RCS-USER 12345679 12345678 IN IP4 10.46.0.7
s=VIDEOCALL
c=IN IP4 10.46.0.7
b=AS:602
b=RS:5800
b=RR:8000
t=0 0
m=audio 7010 RTP/AVP 127 100 105 98 103 96 97
b=AS:42
b=RS:600
b=RR:2000
a=rtpmap:127 EVS/16000/1
a=fmtp:127 max-red=0; br=5.9-24.4; bw=nb-swb
a=rtpmap:100 AMR-WB/16000/1
a=fmtp:100 mode-change-capability=2; max-red=0
a=rtpmap:105 AMR-WB/16000/1
a=fmtp:105 octet-align=1; mode-change-capability=2; max-red=0
a=rtpmap:98 AMR/8000/1
a=fmtp:98 mode-change-capability=2; max-red=0
a=rtpmap:103 AMR/8000/1
a=fmtp:103 octet-align=1; mode-change-capability=2; max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=rtpmap:97 telephone-event/8000
a=fmtp:97 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
m=video 26300 RTP/AVP 115 116 117
b=AS:560
b=RS:5200
b=RR:6000
a=tcap:1 RTP/AVPF
a=pcfg:1 t=1
a=rtpmap:115 H265/90000/1
a=framesize:115 720-1280
a=fmtp:115 profile-id=1; level-id=93; sprop-vps=QAEMAf//AWAAAAMAgAAAAwAAAwBaLAUg; sprop-pps=RAHAcYDZIA==; sprop-sps=QgEBAWAAAAMAgAAAAwAAAwBaoAaiAeFlLktIvQB3CAQQ
a=rtpmap:116 H264/90000/1
a=framesize:116 240-320
a=fmtp:116 profile-level-id=42C00C; packetization-mode=1; sprop-parameter-sets=Z0LADNoPCmQ=,aM4G8g==
a=rtpmap:117 H264/90000/1
a=framesize:117 240-320
a=fmtp:117 profile-level-id=42C00C; packetization-mode=0; sprop-parameter-sets=Z0LADNoPCmQ=,aM4G8g==
a=framerate:33
a=rtcp-fb:* nack
a=rtcp-fb:* nack pli
a=rtcp-fb:* ccm fir
a=rtcp-fb:* ccm tmmbr
a=extmap:7 urn:3gpp:video-orientation
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
a=sendrecv
