INVITE sip:b394eba0-c3ed-4e54-adf4-83d317ca2979@10.46.0.2:43300 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKUBVLP7NEKIL6Z7ONMLVVW73GWX
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKVU72TPLJEOJ2TQYPHTZWAVPH3R
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKSMCBNVGSLAM7KLRD4VMUUKE2BB
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK4PO4NSBIDHUEPLGDU4VA4DJQC2
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKCXAKFOJBWHQLL5JQFCM3FJMBCU
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=582401df
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
Contact: <sip:15550000002@10.46.0.3:49165>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Call-ID: 597b5d5e874d752d@10.46.0.3
CSeq: 1 INVITE
Max-Forwards: 65
Via: SIP/2.0/TCP 10.46.0.3:49165;branch=z9hG4bKf46b14eb951ec6c8a19e6a0
Record-Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5063;transport=tcp;lr;r2=on;did=T7SHDVRJGHLDOKVN;ue>
Record-Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5060;lr;r2=on;did=T7SHDVRJGHLDOKVN>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=XERJQUZ6JZLW2PV7>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=3VYFCAB5PBCJ7SMI>
Record-Route: <sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5060;lr;r2=on;did=ICMM4EZXN4VRLZSX>
Record-Route: <sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5063;transport=tcp;lr;r2=on;did=ICMM4EZXN4VRLZSX;ue>
Content-Length: 837
Session-Expires: 1800;refresher=uac
Min-SE: 90
Allow: INVITE,CANCEL,UPDATE,PRACK,BYE,ACK,MESSAGE,SUBSCRIBE,NOTIFY,PUBLISH,REFER,INFO,OPTIONS
Content-Type: application/sdp
User-Agent: Google_Pixel10a_Android 16_BD6A.251031.001.A4
Supported: 100rel,199,histinfo,precondition,sec-agree,timer
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";video
Session-ID: 16f020d7f8d4504b9c6ef39d63c03de4
Accept: application/sdp,application/3gpp-ims+xml
P-Called-Party-ID: <tel:+15550000005>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000002>

v=0
o=sip:IMS_RCS-USER 12345680 12345678 IN IP4 10.46.0.3
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
