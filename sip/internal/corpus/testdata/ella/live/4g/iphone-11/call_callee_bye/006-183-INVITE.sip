SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.46.0.17:61189;branch=z9hG4bK0nRbxbHqiUAFlI9;rport=57130;received=10.46.0.17
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=PhHy2mVm74
To: <tel:+15550000004>;tag=2129068
Call-ID: 6X2v53NtZZwAowLkKtybtmGk
Session-ID: 4d85ae1797ba81f1f20ab74d84f7d260
CSeq: 1 INVITE
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=43F67AKWDG4B6IJA;ue>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=43F67AKWDG4B6IJA>,<sip:mt@10.80.0.5:5080;lr;did=76C6OH5IQ24DUNSA>,<sip:mo@10.80.0.5:5080;lr;did=ANJ22NL7SCMD4MAX>,<sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;lr;r2=on;did=6ADWKX7IFITQURD2>,<sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;transport=tcp;lr;r2=on;did=6ADWKX7IFITQURD2;ue>
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
o=- 20 1000 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50058 RTP/AVP 99 105
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
