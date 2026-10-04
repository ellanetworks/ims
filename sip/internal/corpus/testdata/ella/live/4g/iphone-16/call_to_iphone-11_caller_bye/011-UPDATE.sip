UPDATE sip:10.46.0.17:61189 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK3NDTZ2ZFLDGGJWCCJFQ6F4B4G3
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKK6TB6DQAHCQOZWDH7N72BM2BWP
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK4ZD2ORV3NHKGXSKNSJYZFKN442
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKGU5BP22LNXK3ZYGXZ5ZWEIBQD2
Supported: 100rel,path,replaces,timer
Session-Expires: 1800
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=pkLYHCuiXI
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=eNVkIOuHGB
Call-ID: Dsw8Vtijvn0c0hE6F0v58hyU
Session-ID: 79ef0028a761b50ffe7180b37b4aa165
Contact: <sip:10.46.0.19:54286>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
CSeq: 3 UPDATE
Via: SIP/2.0/UDP 10.46.0.19:54286;branch=z9hG4bK2zliF37tvc7ptnm;rport=54286;received=10.46.0.19
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=N4RDXIYQXEFFAK2M;lr;r2=on;transport=tcp;ue>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=N4RDXIYQXEFFAK2M;lr;r2=on>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=JGIC5KBB7YZ42PXE>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=XJ6RHQS4NQEPHKU7>
Record-Route: <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5060;did=QKEOJVTHMQZBMM6H;lr;r2=on>
Record-Route: <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5063;did=QKEOJVTHMQZBMM6H;lr;r2=on;ue>
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 66
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 510
Require: precondition

v=0
o=sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org 1791148699 1791148700 IN IP4 10.46.0.19
s=-
c=IN IP4 10.46.0.19
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
a=sendrecv
a=des:qos mandatory local sendrecv
a=curr:qos local sendrecv
a=des:qos mandatory remote sendrecv
a=curr:qos remote none
