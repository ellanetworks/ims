UPDATE sip:10.46.0.20:53718 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK2R5CE75QESJ2IDF65NSRV7ANJW
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKOC2HQTOFE5HUAUZGG77D3POUEE
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKKCXEVZYMCYSZWN4A4ONCJRJI5G
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKD4A5A47OQIXL2Y5EH44VNME667
Supported: 100rel,path,replaces,timer
Session-Expires: 1800
To: <tel:+15550000003>;tag=WggUN1Zgez
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=QpGE5TQdRl
Call-ID: 7AYWxgGuWtCDMe2swNwRLn0O
Session-ID: e0b84dc8daec4660b4ba3b9aac28b3ea
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
CSeq: 3 UPDATE
Via: SIP/2.0/TCP 10.46.0.17:61189;branch=z9hG4bKwD4t9ogBZd2TYrX;rport=57130;received=10.46.0.17
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;did=X4DXWDQSXPCZTKXY;lr;r2=on;ue>
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;did=X4DXWDQSXPCZTKXY;lr;r2=on>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=RUUS54IDH4PXEBYQ>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=ZZUFNMQ2CX6GKUJL>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=YYUA6UBAVLV4MUSX;lr;r2=on>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=YYUA6UBAVLV4MUSX;lr;r2=on;transport=tcp;ue>
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 66
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 496
Require: precondition

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791149192 1791149193 IN IP4 10.46.0.17
s=-
c=IN IP4 10.46.0.17
b=AS:30
b=RS:0
b=RR:0
t=0 0
m=audio 49120 RTP/AVP 116 105
b=AS:30
b=RS:0
b=RR:0
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
a=curr:qos remote none
