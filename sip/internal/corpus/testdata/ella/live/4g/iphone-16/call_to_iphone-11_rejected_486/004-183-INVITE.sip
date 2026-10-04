SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKYCYFPVYSTEUZTASFEXB5BQ5HQ5,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKU6SINXUZQJBE57GV6LBVL4O4BY,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKHDHFGXPCMBCSPO3P56AGERCGGV,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKA7GAPUBSMBS3IHH3BNFVHMQUS3,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKZINMR63PTHK6TN45FK7JRNL653,SIP/2.0/UDP 10.46.0.20:53718;branch=z9hG4bKN1VikI8m0UynGKW;received=10.46.0.20;rport=53718
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=tJdvmTarUV
To: <tel:+15550000006>;tag=rd6Yef0EEf
Call-ID: o6CM3VVHNjCGofUWCFekH1sz
Session-ID: 43533c297b74e8be3905d8c790314a9e
CSeq: 1 INVITE
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=GG4KYAYKNAA7HMJC;lr;r2=on;transport=tcp;ue>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=GG4KYAYKNAA7HMJC;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=SIII5VQBJN23MNML;lr>, <sip:mo@10.80.0.5:5080;did=UTOYQPBSPQJOHGM7;lr>, <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;did=FFF3FSK674IRKXPZ;lr;r2=on>, <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;did=FFF3FSK674IRKXPZ;lr;r2=on;ue>
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
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791149017 0 IN IP4 10.46.0.17
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
