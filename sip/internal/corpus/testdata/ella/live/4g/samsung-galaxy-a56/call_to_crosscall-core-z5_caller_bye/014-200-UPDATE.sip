SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.46.0.5:6100;branch=z9hG4bK-524287-1---6e6e5e7a2cf5140d;rport=6101;transport=TCP;received=10.46.0.5
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5063;transport=tcp;lr;r2=on;did=BVHJA3NCOISW3VSJ;ue>,<sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5060;lr;r2=on;did=BVHJA3NCOISW3VSJ>,<sip:mt@10.80.0.5:5080;lr;did=AXKXF3F4BJY7PTUM>,<sip:mo@10.80.0.5:5080;lr;did=5TPKOWGUUW6Y4JQP>,<sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5060;lr;r2=on;did=RVC3TBNKMWVETGSR>,<sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5063;transport=tcp;lr;r2=on;did=RVC3TBNKMWVETGSR;ue>
To: <sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone>;tag=5939484
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=579fff3c
Call-ID: idFuruxAN21AThXOdDJ0zQ..@10.46.0.5
CSeq: 3 UPDATE
Session-Expires: 1800;refresher=uac
Content-Type: application/sdp
Content-Length: 462
Server: Core-Z5 N1982U.6.02.09.FR00
Contact: <sip:8476f6db-5c6a-4256-a0dd-69db67bd85bd@10.46.0.10:44417>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: timer,precondition
Supported: 100rel,from-change,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS

v=0
o=- 2 1001 IN IP4 10.46.0.10
s=QC VOIP
c=IN IP4 10.46.0.10
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50024 RTP/AVP 116 111
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=0
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
