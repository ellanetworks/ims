SIP/2.0 183 Session Progress
Via: SIP/2.0/UDP 10.46.0.19:54286;branch=z9hG4bK3KR0jxaURNfv1Pw;received=10.46.0.19;rport=54286
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=eNVkIOuHGB
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=pkLYHCuiXI
Call-ID: Dsw8Vtijvn0c0hE6F0v58hyU
Session-ID: 79ef0028a761b50ffe7180b37b4aa165
CSeq: 1 INVITE
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=N4RDXIYQXEFFAK2M;lr;r2=on;transport=tcp;ue>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=N4RDXIYQXEFFAK2M;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=JGIC5KBB7YZ42PXE;lr>, <sip:mo@10.80.0.5:5080;did=XJ6RHQS4NQEPHKU7;lr>, <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5060;did=QKEOJVTHMQZBMM6H;lr;r2=on>, <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5063;did=QKEOJVTHMQZBMM6H;lr;r2=on;ue>
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uas
Require: 100rel,precondition,timer
RSeq: 1
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 525
P-Asserted-Identity: <tel:+15550000006>
P-Asserted-Identity: <sip:+15550000006@ims.mnc001.mcc001.3gppnetwork.org;user=phone>

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791148699 0 IN IP4 10.46.0.17
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
