SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.46.0.17:61189;branch=z9hG4bK1h5AIvUotTPeSMn;rport=57130;received=10.46.0.17
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=0gspRBhnpF
To: <tel:+15550000004>;tag=2226378
Call-ID: THOnWNLR7VIfZr9dHlVFUbGr
Session-ID: f8add290e4f735fa54c3199eda201cbc
CSeq: 1 INVITE
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=KSPJ46SRNXXX3GOS;ue>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=KSPJ46SRNXXX3GOS>,<sip:mt@10.80.0.5:5080;lr;did=K7K44XPFOB6SDUWX>,<sip:mo@10.80.0.5:5080;lr;did=3PHXBD5AX6VHYDGQ>,<sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;lr;r2=on;did=V6XZ6C4O6D62QQZL>,<sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;transport=tcp;lr;r2=on;did=V6XZ6C4O6D62QQZL;ue>
Content-Type: application/sdp
Content-Length: 480
Server: Core-Z5 N1982U.6.02.09.FR00
RSeq: 1
Contact: <sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: 100rel,precondition
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
P-Asserted-Identity: <tel:+15550000004>
P-Asserted-Identity: <sip:+15550000004@ims.mnc001.mcc001.3gppnetwork.org;user=phone>

v=0
o=- 22 1000 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50060 RTP/AVP 99 105
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:99 AMR-WB/16000/1
a=fmtp:99 mode-change-capability=2;max-red=0
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=conf:qos remote sendrecv
a=inactive
a=maxptime:240
a=ptime:20
