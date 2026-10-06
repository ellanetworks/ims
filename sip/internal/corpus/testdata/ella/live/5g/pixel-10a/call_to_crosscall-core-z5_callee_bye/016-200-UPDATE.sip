SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.46.0.3:49165;branch=z9hG4bKe6e53ac12ef110ebe3ffbbe6
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=8b4182f4
To: <tel:15550000005;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=102907630
Call-ID: f234da03b74dd2e5@10.46.0.3
CSeq: 3 UPDATE
Record-Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5063;transport=tcp;lr;r2=on;did=VF2DKULG7AK3QLVK;ue>,<sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5060;lr;r2=on;did=VF2DKULG7AK3QLVK>,<sip:mt@10.80.0.5:5080;lr;did=T5NLXQ2TIB2VD72Z>,<sip:mo@10.80.0.5:5080;lr;did=UKBWOZWSS4TOTU2S>,<sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5060;lr;r2=on;did=I25F2O4WA3UDJRBO>,<sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5063;transport=tcp;lr;r2=on;did=I25F2O4WA3UDJRBO;ue>
Content-Length: 457
Content-Type: application/sdp
Session-ID: 2f535e1210995f73ba5ff1bb6bda34d5
Server: Core-Z5 N1982U.6.02.09.FR00
Contact: <sip:b394eba0-c3ed-4e54-adf4-83d317ca2979@10.46.0.2:43300>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: timer,precondition
Supported: timer,100rel,from-change,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Session-Expires: 1800;refresher=uac

v=0
o=- 9 1001 IN IP4 10.46.0.2
s=QC VOIP
c=IN IP4 10.46.0.2
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50060 RTP/AVP 100 96
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
