SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.46.0.15:43435;branch=z9hG4bK3393532841
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=2787081
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=Mr4KdCcCUm
CSeq: 2787076 UPDATE
Call-ID: 2787074_3068573712@10.46.0.15
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=YSYJBROUP5HBIBLA;lr;r2=on;transport=tcp;ue>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=YSYJBROUP5HBIBLA;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=F2H4B4YNCB4BXZTH;lr>, <sip:mo@10.80.0.5:5080;did=GPBN5ZBRAMU4CUDB;lr>, <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;did=4AXIAFQSPSW2IW6L;lr;r2=on>, <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;did=4AXIAFQSPSW2IW6L;lr;r2=on;transport=tcp;ue>
Require: precondition
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uac
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 524

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791148347 1 IN IP4 10.46.0.17
s=-
c=IN IP4 10.46.0.17
b=AS:41
b=RS:0
b=RR:0
t=0 0
m=audio 49120 RTP/AVP 104 96
b=AS:41
b=RS:0
b=RR:0
a=rtpmap:104 AMR-WB/16000
a=fmtp:104 max-red=0; mode-change-capability=2
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=des:qos mandatory local sendrecv
a=curr:qos local sendrecv
a=des:qos mandatory remote sendrecv
a=curr:qos remote sendrecv
a=conf:qos remote sendrecv
