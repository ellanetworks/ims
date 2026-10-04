UPDATE sip:10.46.0.17:61189 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKUPNMFX5P2SZYHBEEMVXNXAHIYS
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK7UPAJYIUFGTN5H4PLCKWFDH6MC
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKVBC4WY4CAFUXRQZD5COGGFFIJL
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKM7BJR3JV2M5KBAXSEJ27V45TCU
Supported: 100rel,path,replaces,timer
Session-Expires: 1800
To: <tel:+15550000006>;tag=rd6Yef0EEf
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=tJdvmTarUV
Call-ID: o6CM3VVHNjCGofUWCFekH1sz
Session-ID: 43533c297b74e8be3905d8c790314a9e
Contact: <sip:10.46.0.20:53718>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
CSeq: 3 UPDATE
Via: SIP/2.0/UDP 10.46.0.20:53718;branch=z9hG4bK2JLEtArAYnsmylm;rport=53718;received=10.46.0.20
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=GG4KYAYKNAA7HMJC;lr;r2=on;transport=tcp;ue>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=GG4KYAYKNAA7HMJC;lr;r2=on>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=SIII5VQBJN23MNML>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=UTOYQPBSPQJOHGM7>
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;did=FFF3FSK674IRKXPZ;lr;r2=on>
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;did=FFF3FSK674IRKXPZ;lr;r2=on;ue>
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 66
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 510
Require: precondition

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
