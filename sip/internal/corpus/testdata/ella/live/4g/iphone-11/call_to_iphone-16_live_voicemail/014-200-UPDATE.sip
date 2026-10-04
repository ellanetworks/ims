SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.46.0.17:61189;branch=z9hG4bKwD4t9ogBZd2TYrX;received=10.46.0.17;rport=57130
To: <tel:+15550000003>;tag=WggUN1Zgez
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=QpGE5TQdRl
Call-ID: 7AYWxgGuWtCDMe2swNwRLn0O
Session-ID: e0b84dc8daec4660b4ba3b9aac28b3ea
CSeq: 3 UPDATE
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;did=X4DXWDQSXPCZTKXY;lr;r2=on;ue>, <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;did=X4DXWDQSXPCZTKXY;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=RUUS54IDH4PXEBYQ;lr>, <sip:mo@10.80.0.5:5080;did=ZZUFNMQ2CX6GKUJL;lr>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=YYUA6UBAVLV4MUSX;lr;r2=on>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=YYUA6UBAVLV4MUSX;lr;r2=on;transport=tcp;ue>
Require: precondition,timer
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uac
Contact: <sip:10.46.0.20:53718>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 529

v=0
o=sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org 1791149193 1 IN IP4 10.46.0.20
s=-
c=IN IP4 10.46.0.20
b=AS:30
b=RS:375
b=RR:1125
t=0 0
m=audio 49120 RTP/AVP 116 105
b=AS:30
b=RS:375
b=RR:1125
a=rtpmap:116 EVS/16000
a=fmtp:116 br=13.2; bw=nb-wb; max-red=220
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
