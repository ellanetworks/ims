UPDATE sip:10.46.0.20:53718 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKAYR3BFGEJEJP4JDWFFZSZNR3B7
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKDTWHOEOYKCF6UHL7GZTZOJZPOO
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKDEKMDWIIXQ6TWKSOVBH735YUCS
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKSCQFITDEB2YR47VM2LOE4MRTW4
Supported: 100rel,path,replaces,timer
Session-Expires: 1800
To: <tel:+15550000003>;tag=pn7gRZj1q7
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=pWfyXiyFJx
Call-ID: M49i6CXTtFhysg2iGjLihPpr
Session-ID: cc18799e4f5527507d850f597916c35b
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
CSeq: 3 UPDATE
Via: SIP/2.0/TCP 10.46.0.17:61189;branch=z9hG4bKWObaKSJOr9rDJwL;rport=57130;received=10.46.0.17
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;did=7JHJXPSACJEWNFTK;lr;r2=on;ue>
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;did=7JHJXPSACJEWNFTK;lr;r2=on>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=HIOSYATSLNSYBV6K>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=5QYZOYQBM4D4FTHW>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=HTW2JOZARLRVSYAA;lr;r2=on>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=HTW2JOZARLRVSYAA;lr;r2=on;transport=tcp;ue>
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 66
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 496
Require: precondition

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791149234 1791149235 IN IP4 10.46.0.17
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
