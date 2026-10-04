INVITE sip:10.46.0.17:61189 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKQGV2IVQZLDCIUVITHXOBNF3MSW
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKYEMLIZPTTPYEANY7A6F2QRXFIF
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKH4LBKWAXUK6PDVGJDX7HSJL36N
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKIZ2J25XVECXZV7PM4IKHULVHDK
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKUBUEDHCV5K35F6JBBGGUH6MKA7
Accept: application/3gpp-ims+xml,application/sdp
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=eNVkIOuHGB
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
Call-ID: Dsw8Vtijvn0c0hE6F0v58hyU
Session-ID: 79ef0028a761b50ffe7180b37b4aa165
Contact: <sip:10.46.0.19:54286>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
CSeq: 1 INVITE
Via: SIP/2.0/UDP 10.46.0.19:54286;branch=z9hG4bK3KR0jxaURNfv1Pw;rport=54286;received=10.46.0.19
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;transport=tcp;lr;r2=on;did=N4RDXIYQXEFFAK2M;ue>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;lr;r2=on;did=N4RDXIYQXEFFAK2M>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=JGIC5KBB7YZ42PXE>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=XJ6RHQS4NQEPHKU7>
Record-Route: <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5060;lr;r2=on;did=QKEOJVTHMQZBMM6H>
Record-Route: <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5063;lr;r2=on;did=QKEOJVTHMQZBMM6H;ue>
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
o=sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org 1791148699 1791148699 IN IP4 10.46.0.19
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
