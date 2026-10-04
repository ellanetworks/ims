UPDATE sip:10.46.0.17:61189 SIP/2.0
Require: precondition,sec-agree
Supported: 100rel,path,replaces,timer
Session-Expires: 1800
To: <tel:+15550000006>;tag=rd6Yef0EEf
Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;did=FFF3FSK674IRKXPZ;lr;r2=on;ue>, <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;did=FFF3FSK674IRKXPZ;lr;r2=on>, <sip:mo@10.80.0.5:5080;did=UTOYQPBSPQJOHGM7;lr>, <sip:mt@10.80.0.5:5080;did=SIII5VQBJN23MNML;lr>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=GG4KYAYKNAA7HMJC;lr;r2=on>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=GG4KYAYKNAA7HMJC;lr;r2=on;transport=tcp;ue>
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=tJdvmTarUV
Call-ID: o6CM3VVHNjCGofUWCFekH1sz
Session-ID: 43533c297b74e8be3905d8c790314a9e
Contact: <sip:10.46.0.20:53718>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
CSeq: 3 UPDATE
Via: SIP/2.0/UDP 10.46.0.20:53718;branch=z9hG4bK2JLEtArAYnsmylm;rport
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 70
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Security-Verify: ipsec-3gpp;alg=hmac-sha-1-96;ealg=null;mod=trans;port-c=5064;port-s=5063;prot=esp;q=0.1;spi-c=77986854;spi-s=238201135
Proxy-Require: sec-agree
Content-Type: application/sdp
Content-Length: 510

v=0
o=sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org 1791149017 1791149018 IN IP4 10.46.0.20
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
