INVITE sip:10.46.0.17:61189 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKNQOYOPX7F5OEEQAUL7IUXARGHV
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKEKAVGK3LQ4U66NF2YB7JQQFGVH
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKPARLJW555FN4MSEEICJJOQFDYK
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKNAYUWMDRXZ6NC4FMZWSE5PRXPK
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKSY3APQKQFDBTZFIXX23PEE3C6W
Accept: application/3gpp-ims+xml,application/sdp
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=qFoDhZ8t3I
To: <tel:+15550000006>
Call-ID: CbGVybVmwugZ5NcE1udfVl2Z
Session-ID: 214e9273547593d0a7821e7caa4e266d
Contact: <sip:10.46.0.19:54286>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
CSeq: 1 INVITE
Via: SIP/2.0/UDP 10.46.0.19:54286;branch=z9hG4bKN90UrdxexGjQFd4;rport=54286;received=10.46.0.19
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;transport=tcp;lr;r2=on;did=F5UCRKKYIXMYNXA3;ue>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;lr;r2=on;did=F5UCRKKYIXMYNXA3>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=MYUSMYNP223LEG6B>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=V4AD5R6ITUR3MFX6>
Record-Route: <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5060;lr;r2=on;did=SQDVSKQGRKXLGWGR>
Record-Route: <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5063;lr;r2=on;did=SQDVSKQGRKXLGWGR;ue>
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
o=sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org 1791148937 1791148937 IN IP4 10.46.0.19
s=-
c=IN IP4 10.46.0.19
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
