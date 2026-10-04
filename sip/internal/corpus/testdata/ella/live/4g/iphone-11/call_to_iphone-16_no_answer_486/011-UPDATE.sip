UPDATE sip:10.46.0.20:53718 SIP/2.0
Require: precondition,sec-agree
Supported: 100rel,path,replaces,timer
Session-Expires: 1800
To: <tel:+15550000003>;tag=6M2cC6shoR
Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=QQGC5BFXP6ZX3JKD;lr;r2=on;transport=tcp;ue>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=QQGC5BFXP6ZX3JKD;lr;r2=on>, <sip:mo@10.80.0.5:5080;did=PWN32BO6FZUKSXZO;lr>, <sip:mt@10.80.0.5:5080;did=XK5SSXCAFFQYR3Z6;lr>, <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;did=ZB25FCIOS5KMME6C;lr;r2=on>, <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;did=ZB25FCIOS5KMME6C;lr;r2=on;ue>
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=JfQdEX0VUc
Call-ID: gUiVFxQ4a0Ah6UrdbS8lTcmy
Session-ID: 5fbc98a363d0ccc4ad24641f83c911c7
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
CSeq: 3 UPDATE
Via: SIP/2.0/TCP 10.46.0.17:61189;branch=z9hG4bKjgFosKDPnH7iquH;rport
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 70
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Security-Verify: ipsec-3gpp;alg=hmac-sha-1-96;ealg=null;mod=trans;port-c=5064;port-s=5063;prot=esp;q=0.1;spi-c=189860270;spi-s=88237557
Proxy-Require: sec-agree
Content-Type: application/sdp
Content-Length: 496

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791149447 1791149448 IN IP4 10.46.0.17
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
