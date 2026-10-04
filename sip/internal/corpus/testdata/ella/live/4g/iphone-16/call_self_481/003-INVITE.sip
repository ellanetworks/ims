INVITE sip:10.46.0.19:54286 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKMH3EKISKBDLIKBZNHJDFY737SY
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKQCNX6OVQDACKN5BXJFDU7KWJOO
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKJFTKPHZGBSGACYBRNSSVAVQREJ
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKES6ZB6ADG6435RGMEWDVQXZ5PF
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK5IWR7NR7JLX7CDI7237BDWRQ2I
Accept: application/3gpp-ims+xml,application/sdp
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=21m1JIvy5M
To: <tel:+15550000003>
Call-ID: OAMpjVpqF2TE8Fj4Q0LH4lpK
Session-ID: 6fa769bd9f136c112378cfbc781a3ef1
Contact: <sip:10.46.0.19:54286>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
CSeq: 1 INVITE
Via: SIP/2.0/UDP 10.46.0.19:54286;branch=z9hG4bKCKndfLCEn9AjJu7;rport=54286;received=10.46.0.19
Record-Route: <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5063;lr;r2=on;did=NEBAMOMDCKUPII6G;ue>
Record-Route: <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5060;lr;r2=on;did=NEBAMOMDCKUPII6G>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=COVVVZKF6YUG3BOJ>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=XW66SSURMDSEPTTR>
Record-Route: <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5060;lr;r2=on;did=T2A4Z5RBAIJA7HWB>
Record-Route: <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5063;lr;r2=on;did=T2A4Z5RBAIJA7HWB;ue>
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 65
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 815
P-Called-Party-ID: <tel:+15550000003>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000003>

v=0
o=sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org 1791148893 1791148893 IN IP4 10.46.0.19
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
