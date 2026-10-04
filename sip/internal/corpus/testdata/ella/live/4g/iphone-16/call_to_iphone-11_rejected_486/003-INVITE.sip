INVITE sip:10.46.0.17:61189 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKYCYFPVYSTEUZTASFEXB5BQ5HQ5
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKU6SINXUZQJBE57GV6LBVL4O4BY
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKHDHFGXPCMBCSPO3P56AGERCGGV
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKA7GAPUBSMBS3IHH3BNFVHMQUS3
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKZINMR63PTHK6TN45FK7JRNL653
Accept: application/3gpp-ims+xml,application/sdp
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=tJdvmTarUV
To: <tel:+15550000006>
Call-ID: o6CM3VVHNjCGofUWCFekH1sz
Session-ID: 43533c297b74e8be3905d8c790314a9e
Contact: <sip:10.46.0.20:53718>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
CSeq: 1 INVITE
Via: SIP/2.0/UDP 10.46.0.20:53718;branch=z9hG4bKN1VikI8m0UynGKW;rport=53718;received=10.46.0.20
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;transport=tcp;lr;r2=on;did=GG4KYAYKNAA7HMJC;ue>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;lr;r2=on;did=GG4KYAYKNAA7HMJC>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=SIII5VQBJN23MNML>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=UTOYQPBSPQJOHGM7>
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;lr;r2=on;did=FFF3FSK674IRKXPZ>
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;lr;r2=on;did=FFF3FSK674IRKXPZ;ue>
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 65
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 815
P-Called-Party-ID: <tel:+15550000006>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000003>

v=0
o=sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org 1791149017 1791149017 IN IP4 10.46.0.20
s=-
c=IN IP4 10.46.0.20
b=AS:42
b=RS:525
b=RR:1575
t=0 0
m=audio 49120 RTP/AVP 116 96 99 97 105 100
b=AS:42
b=RS:525
b=RR:1575
a=rtpmap:116 EVS/16000
a=fmtp:116 br=5.9-24.4; bw=nb-swb; max-red=220; mode-set=0,1,2
a=rtpmap:96 EVS/16000
a=fmtp:96 br=5.9-24.4; bw=nb-swb; max-red=220; mode-set=0,1,2
a=rtpmap:99 AMR-WB/16000
a=fmtp:99 max-red=0; mode-change-capability=2
a=rtpmap:97 AMR/8000
a=fmtp:97 max-red=0; mode-change-capability=2
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=rtpmap:100 telephone-event/8000
a=fmtp:100 0-15
a=ptime:20
a=maxptime:240
a=inactive
a=des:qos mandatory local sendrecv
a=curr:qos local none
a=des:qos optional remote sendrecv
a=curr:qos remote none
