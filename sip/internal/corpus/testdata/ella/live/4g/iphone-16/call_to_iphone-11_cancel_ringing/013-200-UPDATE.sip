SIP/2.0 200 OK
Via: SIP/2.0/UDP 10.46.0.20:53718;branch=z9hG4bKmmsindAaGLaTdNV;received=10.46.0.20;rport=53718
To: <tel:+15550000006>;tag=TIx1YlH5tS
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=rDu5XCAcI7
Call-ID: 6CNYuJw7CaT0ZgcdFudaAnfo
Session-ID: 31f0475347d59a334a5b0300fe887c23
CSeq: 3 UPDATE
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=3LADSZ64ZU26ZDNN;lr;r2=on;transport=tcp;ue>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=3LADSZ64ZU26ZDNN;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=TRDMZTJYDTFUPYID;lr>, <sip:mo@10.80.0.5:5080;did=JHBFDAGIDSRNIHSH;lr>, <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;did=L5AXLJJDTGNLRPMO;lr;r2=on>, <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;did=L5AXLJJDTGNLRPMO;lr;r2=on;ue>
Require: precondition,timer
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uas
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 523

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791149074 1 IN IP4 10.46.0.17
s=-
c=IN IP4 10.46.0.17
b=AS:42
b=RS:0
b=RR:0
t=0 0
m=audio 49120 RTP/AVP 116 105
b=AS:42
b=RS:0
b=RR:0
a=rtpmap:116 EVS/16000
a=fmtp:116 br=5.9-24.4; bw=nb-wb; max-red=220
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=des:qos mandatory local sendrecv
a=curr:qos local sendrecv
a=des:qos mandatory remote sendrecv
a=curr:qos remote sendrecv
a=conf:qos remote sendrecv
