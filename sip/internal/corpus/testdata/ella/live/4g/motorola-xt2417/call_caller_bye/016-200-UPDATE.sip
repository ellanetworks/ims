SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.46.0.14:40265;branch=z9hG4bK3111565003
From: <sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org>;tag=714593
To: <sip:15550000004;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=715026
CSeq: 714588 UPDATE
Call-ID: 714586_4111201712@10.46.0.14
Content-Length: 453
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=WBHZ5CXBTNINNCXK;ue>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=WBHZ5CXBTNINNCXK>,<sip:mt@10.80.0.5:5080;lr;did=CN5VYNJ7TIKGNMSI>,<sip:mo@10.80.0.5:5080;lr;did=7OAQAI477SM3ORMU>,<sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;lr;r2=on;did=5NBGRQBGJZQUKTP2>,<sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;transport=tcp;lr;r2=on;did=5NBGRQBGJZQUKTP2;ue>
Content-Type: application/sdp
Server: Core-Z5 N1982U.6.02.09.FR00
Contact: <sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition
Supported: timer,100rel,from-change,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Session-Expires: 1800;refresher=uas

v=0
o=- 11 1001 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50026 RTP/AVP 99 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:99 AMR-WB/16000/1
a=fmtp:99 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local none
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
