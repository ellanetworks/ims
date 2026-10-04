SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKHVDEU3SHD4PJV5QGWX7D2BSWRG,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKHSVN6V3INC3TX5UINGWC7D6NBT,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKYFDGGUP6GYKXNJFHPFBTSQKLBH,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK2YWFALJF6FEZWM7RNZYUQKCHW2,SIP/2.0/TCP 10.46.0.17:61189;branch=z9hG4bKtBLMSw3aPRLT1Q2;rport=57130;received=10.46.0.17
To: <tel:+15550000004>;tag=2029281
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=phGRFFh1Oz
Call-ID: XLSDSgMpObRgVituc0pOtPU2
Session-ID: b9ef98e3980320464cdc5f1eb9bf4d05
CSeq: 3 UPDATE
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;did=IM3Z7F4LVJAYGX6Z;lr;r2=on;transport=tcp;ue>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;did=IM3Z7F4LVJAYGX6Z;lr;r2=on>,<sip:mt@10.80.0.5:5080;lr;did=4R65NXDM3N6HEDHH>,<sip:mo@10.80.0.5:5080;lr;did=IVINAR5TEFPZ4SSM>,<sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=VMJQVCKFZ2ERSM43;lr;r2=on>,<sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=VMJQVCKFZ2ERSM43;lr;r2=on;transport=tcp;ue>
Content-Type: application/sdp
Content-Length: 450
Server: Core-Z5 N1982U.6.02.09.FR00
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015EE0000
Contact: <sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: timer,precondition
Supported: timer,100rel,from-change,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Session-Expires: 1800;refresher=uac

v=0
o=- 19 1001 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:41
b=RS:0
b=RR:0
t=0 0
m=audio 50024 RTP/AVP 99 105
b=AS:41
b=RS:0
b=RR:0
a=rtpmap:99 AMR-WB/16000/1
a=fmtp:99 mode-change-capability=2;max-red=0
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
