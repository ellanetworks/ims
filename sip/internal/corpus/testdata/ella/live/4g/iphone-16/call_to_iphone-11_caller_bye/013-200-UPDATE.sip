SIP/2.0 200 OK
Via: SIP/2.0/UDP 10.46.0.19:54286;branch=z9hG4bK2zliF37tvc7ptnm;received=10.46.0.19;rport=54286
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=pkLYHCuiXI
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=eNVkIOuHGB
Call-ID: Dsw8Vtijvn0c0hE6F0v58hyU
Session-ID: 79ef0028a761b50ffe7180b37b4aa165
CSeq: 3 UPDATE
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=N4RDXIYQXEFFAK2M;lr;r2=on;transport=tcp;ue>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=N4RDXIYQXEFFAK2M;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=JGIC5KBB7YZ42PXE;lr>, <sip:mo@10.80.0.5:5080;did=XJ6RHQS4NQEPHKU7;lr>, <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5060;did=QKEOJVTHMQZBMM6H;lr;r2=on>, <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5063;did=QKEOJVTHMQZBMM6H;lr;r2=on;ue>
Require: precondition,timer
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uas
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 519

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791148699 1 IN IP4 10.46.0.17
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
a=curr:qos local none
a=des:qos mandatory remote sendrecv
a=curr:qos remote sendrecv
a=conf:qos remote sendrecv
