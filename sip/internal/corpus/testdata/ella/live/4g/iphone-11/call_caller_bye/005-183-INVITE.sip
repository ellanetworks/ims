SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK6YFFAF3GG3DTCSOEB42YU2YTXA,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKDSRU2QXM72VY4EWFWE5GGIOTMA,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKLSLG6RKSFTIFV7AFL3RB2TIBBV,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKKBBSUIXN5TQKZQS5NTBKDOFFVV,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKE27WD46KIHCDIEADP2Q3OLUWPT,SIP/2.0/TCP 10.46.0.17:61189;branch=z9hG4bKgvp94yH5Z9mDVrD;rport=57130;received=10.46.0.17
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=phGRFFh1Oz
To: <tel:+15550000004>;tag=2029281
Call-ID: XLSDSgMpObRgVituc0pOtPU2
Session-ID: b9ef98e3980320464cdc5f1eb9bf4d05
CSeq: 1 INVITE
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=IM3Z7F4LVJAYGX6Z;ue>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=IM3Z7F4LVJAYGX6Z>,<sip:mt@10.80.0.5:5080;lr;did=4R65NXDM3N6HEDHH>,<sip:mo@10.80.0.5:5080;lr;did=IVINAR5TEFPZ4SSM>,<sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;lr;r2=on;did=VMJQVCKFZ2ERSM43>,<sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;transport=tcp;lr;r2=on;did=VMJQVCKFZ2ERSM43;ue>
Content-Type: application/sdp
Content-Length: 480
Server: Core-Z5 N1982U.6.02.09.FR00
RSeq: 1
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015EE0000
Contact: <sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: 100rel,precondition
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS

v=0
o=- 19 1000 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50024 RTP/AVP 99 105
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
