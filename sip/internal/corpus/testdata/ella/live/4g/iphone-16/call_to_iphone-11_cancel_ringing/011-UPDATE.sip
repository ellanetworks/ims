UPDATE sip:10.46.0.17:61189 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKBILFATLLOQ2OG372NFPLQFWYQZ
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK2ZKH4OCM3H2745T3TV64A4RVAP
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKCNCD2UUR36CJGNV5S6UY4YWX3U
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK4NNSYUNYIOC6KGEIQ3HA6UFQWK
Supported: 100rel,path,replaces,timer
Session-Expires: 1800
To: <tel:+15550000006>;tag=TIx1YlH5tS
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=rDu5XCAcI7
Call-ID: 6CNYuJw7CaT0ZgcdFudaAnfo
Session-ID: 31f0475347d59a334a5b0300fe887c23
Contact: <sip:10.46.0.20:53718>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
CSeq: 3 UPDATE
Via: SIP/2.0/UDP 10.46.0.20:53718;branch=z9hG4bKmmsindAaGLaTdNV;rport=53718;received=10.46.0.20
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=3LADSZ64ZU26ZDNN;lr;r2=on;transport=tcp;ue>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=3LADSZ64ZU26ZDNN;lr;r2=on>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=TRDMZTJYDTFUPYID>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=JHBFDAGIDSRNIHSH>
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;did=L5AXLJJDTGNLRPMO;lr;r2=on>
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;did=L5AXLJJDTGNLRPMO;lr;r2=on;ue>
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 66
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 510
Require: precondition

v=0
o=sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org 1791149074 1791149075 IN IP4 10.46.0.20
s=-
c=IN IP4 10.46.0.20
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
a=sendrecv
a=des:qos mandatory local sendrecv
a=curr:qos local sendrecv
a=des:qos mandatory remote sendrecv
a=curr:qos remote none
