INVITE sip:10.46.0.20:53718 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKK5PEBWCQZZY6VPRMJL3JKCNX4O
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKQE2JFQ6ZTJIAK4ETHNZIMYHDHC
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKF5G625JB4QSW3RHOHB5WS2N2QN
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKJBTW6R5G6ACY4TV2A6PZSE36SS
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKIUU3QJL44WAOX7BZBLFLHAMS22
Accept: application/3gpp-ims+xml,application/sdp
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Supported: 100rel,path,precondition,replaces,timer
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=pWfyXiyFJx
To: <tel:+15550000003>
Call-ID: M49i6CXTtFhysg2iGjLihPpr
Session-ID: cc18799e4f5527507d850f597916c35b
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
CSeq: 1 INVITE
Via: SIP/2.0/TCP 10.46.0.17:61189;branch=z9hG4bKX59VYrjOdi1yVnv;rport=57130;received=10.46.0.17
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;lr;r2=on;did=7JHJXPSACJEWNFTK;ue>
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;lr;r2=on;did=7JHJXPSACJEWNFTK>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=HIOSYATSLNSYBV6K>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=5QYZOYQBM4D4FTHW>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;lr;r2=on;did=HTW2JOZARLRVSYAA>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;transport=tcp;lr;r2=on;did=HTW2JOZARLRVSYAA;ue>
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 65
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 705
P-Called-Party-ID: <tel:+15550000003>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000006>

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791149234 1791149234 IN IP4 10.46.0.17
s=-
c=IN IP4 10.46.0.17
b=AS:41
b=RS:512
b=RR:1537
t=0 0
m=audio 49120 RTP/AVP 116 99 97 105 100
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:116 EVS/16000
a=fmtp:116 br=13.2; bw=nb-wb; max-red=220
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
