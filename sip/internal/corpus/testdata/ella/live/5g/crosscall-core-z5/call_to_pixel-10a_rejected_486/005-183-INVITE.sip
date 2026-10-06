SIP/2.0 183 Session Progress
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=103310767
To: <tel:15550000002;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=b3bbc289
Contact: <sip:15550000002@10.46.0.3:49165>;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video
Call-ID: 103310760_1760534736@10.46.0.2
CSeq: 103310760 INVITE
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKW7EHSPVRK2M5X62FOMKNAR6Z4A,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKCPRZOS5MLDXXOOW25RV4N2GGKW,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKZJAFPBYNPECZN3SOV2RLV4BKRB,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKS5YRYSKXPDLI7RSPJ6U4G6MB44,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKYPI7GLMTIL37TIPIIQIPDFRFHN,SIP/2.0/TCP 10.46.0.2:43300;branch=z9hG4bK1922361788
Require: 100rel,precondition
Content-Length: 475
Supported: 100rel,precondition,timer
Allow: INVITE,CANCEL,UPDATE,PRACK,BYE,ACK,MESSAGE,SUBSCRIBE,NOTIFY,PUBLISH,REFER,INFO,OPTIONS
Content-Type: application/sdp
P-Preferred-Identity: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=0010100000100066c000
Server: Google_Pixel10a_Android 16_BD6A.251031.001.A4
Record-Route: <sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5063;transport=tcp;lr;r2=on;ob;did=P3M3RQZAT22TNGN3;ue>,<sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5060;lr;r2=on;ob;did=P3M3RQZAT22TNGN3>,<sip:mt@10.80.0.5:5080;lr;did=I74XPGSQO32BRJPH>,<sip:mo@10.80.0.5:5080;lr;did=YHNIGHXT5KSXB5PT>,<sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5060;lr;r2=on;did=KRJRUFGOJHVZWXI2>,<sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5063;transport=tcp;lr;r2=on;did=KRJRUFGOJHVZWXI2;ue>
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
