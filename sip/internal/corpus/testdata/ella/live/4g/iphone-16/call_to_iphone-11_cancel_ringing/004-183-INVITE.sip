SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK6X2ANSR4GBQ2UIDDKDROJ4AQDA,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKO37QLZI6AEIZQ7RTTCXRCIUT2O,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK5QUJCBRUUAL2EFPUEXOQEWV7N7,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKKFYFMADPDVVRRWRE5NN5XGJYZ7,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK4MCHVCOBB34RACSXY2TELT7IQB,SIP/2.0/UDP 10.46.0.20:53718;branch=z9hG4bK0r8tMtEYMwTwe3G;received=10.46.0.20;rport=53718
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=rDu5XCAcI7
To: <tel:+15550000006>;tag=TIx1YlH5tS
Call-ID: 6CNYuJw7CaT0ZgcdFudaAnfo
Session-ID: 31f0475347d59a334a5b0300fe887c23
CSeq: 1 INVITE
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=3LADSZ64ZU26ZDNN;lr;r2=on;transport=tcp;ue>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=3LADSZ64ZU26ZDNN;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=TRDMZTJYDTFUPYID;lr>, <sip:mo@10.80.0.5:5080;did=JHBFDAGIDSRNIHSH;lr>, <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;did=L5AXLJJDTGNLRPMO;lr;r2=on>, <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;did=L5AXLJJDTGNLRPMO;lr;r2=on;ue>
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uas
Require: 100rel,precondition,timer
RSeq: 1
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Content-Type: application/sdp
Content-Length: 525

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791149074 0 IN IP4 10.46.0.17
s=-
c=IN IP4 10.46.0.17
b=AS:42
b=RS:525
b=RR:1575
t=0 0
m=audio 49120 RTP/AVP 116 105
b=AS:42
b=RS:525
b=RR:1575
a=rtpmap:116 EVS/16000
a=fmtp:116 br=5.9-24.4; bw=nb-wb; max-red=220
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=ptime:20
a=maxptime:240
a=inactive
a=des:qos mandatory local sendrecv
a=curr:qos local none
a=des:qos mandatory remote sendrecv
a=curr:qos remote none
a=conf:qos remote sendrecv
