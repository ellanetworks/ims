UPDATE sip:b394eba0-c3ed-4e54-adf4-83d317ca2979@10.46.0.2:43300 SIP/2.0
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=8b4182f4
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=102907630
Contact: <sip:15550000002@10.46.0.3:49165>;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video
Call-ID: f234da03b74dd2e5@10.46.0.3
CSeq: 3 UPDATE
Max-Forwards: 70
Via: SIP/2.0/TCP 10.46.0.3:49165;branch=z9hG4bKe6e53ac12ef110ebe3ffbbe6
Content-Length: 451
Content-Type: application/sdp
Require: precondition,sec-agree
Supported: 100rel,199,histinfo,precondition,sec-agree,timer
Allow: INVITE,CANCEL,UPDATE,PRACK,BYE,ACK,MESSAGE,SUBSCRIBE,NOTIFY,PUBLISH,REFER,INFO,OPTIONS
Proxy-Require: sec-agree
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=0010100000100066c000
User-Agent: Google_Pixel10a_Android 16_BD6A.251031.001.A4
Route: <sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5063;transport=tcp;lr;r2=on;did=I25F2O4WA3UDJRBO;ue>,<sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5060;lr;r2=on;did=I25F2O4WA3UDJRBO>,<sip:mo@10.80.0.5:5080;lr;did=UKBWOZWSS4TOTU2S>,<sip:mt@10.80.0.5:5080;lr;did=T5NLXQ2TIB2VD72Z>,<sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5060;lr;r2=on;did=VF2DKULG7AK3QLVK>,<sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5063;transport=tcp;lr;r2=on;did=VF2DKULG7AK3QLVK;ue>
Session-ID: 2f535e1210995f73ba5ff1bb6bda34d5
Security-Verify: ipsec-3gpp;q=0.1;alg=hmac-sha-1-96;prot=esp;mod=trans;ealg=null;spi-c=6454639;spi-s=197865269;port-c=5064;port-s=5063

v=0
o=sip:IMS_RCS-USER 12345679 12345679 IN IP4 10.46.0.3
s=VOLTECALL
c=IN IP4 10.46.0.3
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
