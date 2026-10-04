UPDATE sip:10.46.0.17:61189 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKHG5662T5AFARCB6ET2KDXX7CTM
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKOIY5NFOFZMSKPIXLJA75VNAXMD
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKQGU3XKOL5O52DMFGH5COJYAOGK
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKBJ2OE2TXGTALNGSD7CJLZ6S2MM
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=2709695
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=GdQCQwRQqj
CSeq: 2709690 UPDATE
Call-ID: 2709688_3069114096@10.46.0.15
Content-Length: 446
Via: SIP/2.0/TCP 10.46.0.15:43435;branch=z9hG4bK412788296
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=WPUGTEUSZUEI3WKZ;lr;r2=on;transport=tcp;ue>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=WPUGTEUSZUEI3WKZ;lr;r2=on>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=WYR7FTS4PGQYBJFH>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=JYUGX4TC663PQD5G>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;did=2IGOGQRNOJOD6PWC;lr;r2=on>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;did=2IGOGQRNOJOD6PWC;lr;r2=on;transport=tcp;ue>
Max-Forwards: 66
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Contact: <sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition

v=0
o=- 26 1001 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:41
b=RS:0
b=RR:0
t=0 0
m=audio 50050 RTP/AVP 104 96
b=AS:41
b=RS:0
b=RR:0
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
