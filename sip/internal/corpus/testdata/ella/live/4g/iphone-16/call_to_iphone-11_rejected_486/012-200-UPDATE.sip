SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKUPNMFX5P2SZYHBEEMVXNXAHIYS,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK7UPAJYIUFGTN5H4PLCKWFDH6MC,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKVBC4WY4CAFUXRQZD5COGGFFIJL,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKM7BJR3JV2M5KBAXSEJ27V45TCU,SIP/2.0/UDP 10.46.0.20:53718;branch=z9hG4bK2JLEtArAYnsmylm;received=10.46.0.20;rport=53718
To: <tel:+15550000006>;tag=rd6Yef0EEf
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=tJdvmTarUV
Call-ID: o6CM3VVHNjCGofUWCFekH1sz
Session-ID: 43533c297b74e8be3905d8c790314a9e
CSeq: 3 UPDATE
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=GG4KYAYKNAA7HMJC;lr;r2=on;transport=tcp;ue>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=GG4KYAYKNAA7HMJC;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=SIII5VQBJN23MNML;lr>, <sip:mo@10.80.0.5:5080;did=UTOYQPBSPQJOHGM7;lr>, <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;did=FFF3FSK674IRKXPZ;lr;r2=on>, <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;did=FFF3FSK674IRKXPZ;lr;r2=on;ue>
Require: precondition,timer
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uas
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Content-Type: application/sdp
Content-Length: 523

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791149017 1 IN IP4 10.46.0.17
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
