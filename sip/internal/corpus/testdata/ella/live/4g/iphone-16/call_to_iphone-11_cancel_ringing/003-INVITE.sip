INVITE sip:10.46.0.17:61189 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK6X2ANSR4GBQ2UIDDKDROJ4AQDA
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKO37QLZI6AEIZQ7RTTCXRCIUT2O
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK5QUJCBRUUAL2EFPUEXOQEWV7N7
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKKFYFMADPDVVRRWRE5NN5XGJYZ7
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK4MCHVCOBB34RACSXY2TELT7IQB
Accept: application/3gpp-ims+xml,application/sdp
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=rDu5XCAcI7
To: <tel:+15550000006>
Call-ID: 6CNYuJw7CaT0ZgcdFudaAnfo
Session-ID: 31f0475347d59a334a5b0300fe887c23
Contact: <sip:10.46.0.20:53718>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
CSeq: 1 INVITE
Via: SIP/2.0/UDP 10.46.0.20:53718;branch=z9hG4bK0r8tMtEYMwTwe3G;rport=53718;received=10.46.0.20
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;transport=tcp;lr;r2=on;did=3LADSZ64ZU26ZDNN;ue>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;lr;r2=on;did=3LADSZ64ZU26ZDNN>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=TRDMZTJYDTFUPYID>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=JHBFDAGIDSRNIHSH>
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;lr;r2=on;did=L5AXLJJDTGNLRPMO>
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;lr;r2=on;did=L5AXLJJDTGNLRPMO;ue>
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
o=sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org 1791149074 1791149074 IN IP4 10.46.0.20
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
