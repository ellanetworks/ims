INVITE sip:b394eba0-c3ed-4e54-adf4-83d317ca2979@10.46.0.2:43300 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKXQJNM2BZJGJXHKWJ5NOQNERYT3
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKUXEVHCSJ25IAEVBW3L3KXURUEF
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK532GPM6EMLJTR7OW4MTONV5HSR
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKBMBI5V47D5OSI2B7L4EVQWFNIU
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK5UIYDIAMB4TJPAFL7Z25YIATGI
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=8b4182f4
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
Contact: <sip:15550000002@10.46.0.3:49165>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Call-ID: f234da03b74dd2e5@10.46.0.3
CSeq: 1 INVITE
Max-Forwards: 65
Via: SIP/2.0/TCP 10.46.0.3:49165;branch=z9hG4bKc85c037a1ad739c814fedd4b
Record-Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5063;transport=tcp;lr;r2=on;did=VF2DKULG7AK3QLVK;ue>
Record-Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5060;lr;r2=on;did=VF2DKULG7AK3QLVK>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=T5NLXQ2TIB2VD72Z>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=UKBWOZWSS4TOTU2S>
Record-Route: <sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5060;lr;r2=on;did=I25F2O4WA3UDJRBO>
Record-Route: <sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5063;transport=tcp;lr;r2=on;did=I25F2O4WA3UDJRBO;ue>
Content-Length: 837
Session-Expires: 1800;refresher=uac
Min-SE: 90
Allow: INVITE,CANCEL,UPDATE,PRACK,BYE,ACK,MESSAGE,SUBSCRIBE,NOTIFY,PUBLISH,REFER,INFO,OPTIONS
Content-Type: application/sdp
User-Agent: Google_Pixel10a_Android 16_BD6A.251031.001.A4
Supported: 100rel,199,histinfo,precondition,sec-agree,timer
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";video
Session-ID: 2f535e1210995f73ba5ff1bb6bda34d5
Accept: application/sdp,application/3gpp-ims+xml
P-Called-Party-ID: <tel:+15550000005>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000002>

v=0
o=sip:IMS_RCS-USER 12345679 12345678 IN IP4 10.46.0.3
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
