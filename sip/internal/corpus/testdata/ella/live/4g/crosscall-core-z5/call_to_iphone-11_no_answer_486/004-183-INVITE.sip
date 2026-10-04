SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKLMFQZKHGTYYXIMO6GTCQ774S24,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKRW7LJ3LUYABTV54LKB35G4SKYY,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKMQQVMZOVQBEZODLYBOA6OZKYFP,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKNGOS3KMOQPQ5RUWA4VHF7HVCPM,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKKCOJ3HVN6XJA63HKDAIDT5NUQR,SIP/2.0/TCP 10.46.0.15:43435;branch=z9hG4bK1901442183
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=2787081
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=Mr4KdCcCUm
CSeq: 2787074 INVITE
Call-ID: 2787074_3068573712@10.46.0.15
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=YSYJBROUP5HBIBLA;lr;r2=on;transport=tcp;ue>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=YSYJBROUP5HBIBLA;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=F2H4B4YNCB4BXZTH;lr>, <sip:mo@10.80.0.5:5080;did=GPBN5ZBRAMU4CUDB;lr>, <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;did=4AXIAFQSPSW2IW6L;lr;r2=on>, <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;did=4AXIAFQSPSW2IW6L;lr;r2=on;transport=tcp;ue>
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uac
Require: 100rel,precondition,timer
RSeq: 1
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Content-Type: application/sdp
Content-Length: 516

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791148347 0 IN IP4 10.46.0.17
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
a=curr:qos local none
a=des:qos mandatory remote sendrecv
a=curr:qos remote none
a=conf:qos remote sendrecv
