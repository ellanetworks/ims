UPDATE sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKNEXN5JEJ37522H5BHMUT444SGS
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKELMKF4V7VZLZU3WTLXTFSSB2UM
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK5ZOTGTH2UJ2X26MYMTVVEX5QPY
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKSDMNC3ZTGYWK4SSZFJEFF457OM
Supported: 100rel,path,replaces,timer
To: <tel:+15550000004>;tag=2174542
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=jSRKFYfs1s
Call-ID: Nsgc5oCtKjgMI5fk2MHu6AqX
Session-ID: 028701512bb6973064df15a13a42ee46
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
CSeq: 3 UPDATE
Via: SIP/2.0/TCP 10.46.0.17:61189;branch=z9hG4bKgBCsf36raEnVFrb;rport=57130;received=10.46.0.17
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;did=SUIG74YXVNE7TLHS;lr;r2=on;transport=tcp;ue>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;did=SUIG74YXVNE7TLHS;lr;r2=on>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=4Q4IZ4EGPM2LPYDR>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=F2W6SLKFQSUXMEJP>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=MBJR2L6DI2TMOCR3;lr;r2=on>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=MBJR2L6DI2TMOCR3;lr;r2=on;transport=tcp;ue>
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 66
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 501
Require: precondition

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791147733 1791147734 IN IP4 10.46.0.17
s=-
c=IN IP4 10.46.0.17
b=AS:41
b=RS:0
b=RR:0
t=0 0
m=audio 49120 RTP/AVP 99 105
b=AS:41
b=RS:0
b=RR:0
a=rtpmap:99 AMR-WB/16000
a=fmtp:99 max-red=0; mode-change-capability=2
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=des:qos mandatory local sendrecv
a=curr:qos local sendrecv
a=des:qos mandatory remote sendrecv
a=curr:qos remote none
