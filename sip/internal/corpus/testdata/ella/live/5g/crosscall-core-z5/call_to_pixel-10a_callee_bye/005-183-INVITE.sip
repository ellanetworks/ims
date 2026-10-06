SIP/2.0 183 Session Progress
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=103226179
To: <tel:15550000002;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=5c8299b5
Contact: <sip:15550000002@10.46.0.3:49165>;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video
Call-ID: 103226171_1760455216@10.46.0.2
CSeq: 103226171 INVITE
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKTKHRPVQSVBESCENM5DM5H5H7RS,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK5KULRAFS7MGGOJVXQYC4IRKNIQ,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK37IUL3MRLJDICFHAIGO3WUAKMK,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK5D727VJNCQVSCAEOXZKOO7WZCM,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKFTAREBNUOE32WQTQHWZDI3R3VY,SIP/2.0/TCP 10.46.0.2:43300;branch=z9hG4bK2129445914
Require: 100rel,precondition
Content-Length: 475
Supported: 100rel,precondition,timer
Allow: INVITE,CANCEL,UPDATE,PRACK,BYE,ACK,MESSAGE,SUBSCRIBE,NOTIFY,PUBLISH,REFER,INFO,OPTIONS
Content-Type: application/sdp
P-Preferred-Identity: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=0010100000100066c000
Server: Google_Pixel10a_Android 16_BD6A.251031.001.A4
Record-Route: <sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5063;transport=tcp;lr;r2=on;ob;did=S6VRSB3XEKUJSGRP;ue>,<sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5060;lr;r2=on;ob;did=S6VRSB3XEKUJSGRP>,<sip:mt@10.80.0.5:5080;lr;did=ZSFH365MXCMK6S6R>,<sip:mo@10.80.0.5:5080;lr;did=AFZUARVXGVYCAC6E>,<sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5060;lr;r2=on;did=PPKZDRPPJIR3IS4W>,<sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5063;transport=tcp;lr;r2=on;did=PPKZDRPPJIR3IS4W;ue>
RSeq: 1

v=0
o=sip:IMS_RCS-USER 12345682 12345679 IN IP4 10.46.0.3
s=VOLTECALL
c=IN IP4 10.46.0.3
t=0 0
m=audio 7010 RTP/AVP 104 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2; max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=conf:qos remote sendrecv
