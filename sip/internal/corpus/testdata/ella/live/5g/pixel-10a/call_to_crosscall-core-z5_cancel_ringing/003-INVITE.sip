INVITE sip:b394eba0-c3ed-4e54-adf4-83d317ca2979@10.46.0.2:43300 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK4Z5VL5QSDBBCEDOFWXZP4JIIA2
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKRKE5ENLQBBBRASUQD7JCR5KWF3
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKZAJQGLS6SPDMQVLGOWVMOS2APF
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKG4MUZEEO5SKXZ2UCM6JI3WAIAA
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKMV2QIOHYJXYZIJBTWO3WG7TYZD
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=f3fba2c9
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
Contact: <sip:15550000002@10.46.0.3:49165>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Call-ID: 328a0d67c51df36c@10.46.0.3
CSeq: 1 INVITE
Max-Forwards: 65
Via: SIP/2.0/TCP 10.46.0.3:49165;branch=z9hG4bK28e0a92c52375804a2dd347c
Record-Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5063;transport=tcp;lr;r2=on;did=LANHD46HRCZAIJHW;ue>
Record-Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5060;lr;r2=on;did=LANHD46HRCZAIJHW>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=CKZF4B4Q4KNTJFU3>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=MDAACB6W7KGDQU5I>
Record-Route: <sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5060;lr;r2=on;did=QQPRLPCU4YQ4EQ6B>
Record-Route: <sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5063;transport=tcp;lr;r2=on;did=QQPRLPCU4YQ4EQ6B;ue>
Content-Length: 837
Session-Expires: 1800;refresher=uac
Min-SE: 90
Allow: INVITE,CANCEL,UPDATE,PRACK,BYE,ACK,MESSAGE,SUBSCRIBE,NOTIFY,PUBLISH,REFER,INFO,OPTIONS
Content-Type: application/sdp
User-Agent: Google_Pixel10a_Android 16_BD6A.251031.001.A4
Supported: 100rel,199,histinfo,precondition,sec-agree,timer
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";video
Session-ID: 9bc41936e65d546181bf36c36d83457b
Accept: application/sdp,application/3gpp-ims+xml
P-Called-Party-ID: <tel:+15550000005>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000002>

v=0
o=sip:IMS_RCS-USER 12345681 12345678 IN IP4 10.46.0.3
s=VOLTECALL
c=IN IP4 10.46.0.3
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
