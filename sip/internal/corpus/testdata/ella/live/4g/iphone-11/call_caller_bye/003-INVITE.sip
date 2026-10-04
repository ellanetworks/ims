INVITE sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK6YFFAF3GG3DTCSOEB42YU2YTXA
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKDSRU2QXM72VY4EWFWE5GGIOTMA
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKLSLG6RKSFTIFV7AFL3RB2TIBBV
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKKBBSUIXN5TQKZQS5NTBKDOFFVV
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKE27WD46KIHCDIEADP2Q3OLUWPT
Accept: application/3gpp-ims+xml,application/sdp
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Supported: 100rel,path,precondition,replaces,timer
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=phGRFFh1Oz
To: <tel:+15550000004>
Call-ID: XLSDSgMpObRgVituc0pOtPU2
Session-ID: b9ef98e3980320464cdc5f1eb9bf4d05
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
CSeq: 1 INVITE
Via: SIP/2.0/TCP 10.46.0.17:61189;branch=z9hG4bKgvp94yH5Z9mDVrD;rport=57130;received=10.46.0.17
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=IM3Z7F4LVJAYGX6Z;ue>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=IM3Z7F4LVJAYGX6Z>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=4R65NXDM3N6HEDHH>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=IVINAR5TEFPZ4SSM>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;lr;r2=on;did=VMJQVCKFZ2ERSM43>
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;transport=tcp;lr;r2=on;did=VMJQVCKFZ2ERSM43;ue>
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 65
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 705
P-Called-Party-ID: <tel:+15550000004>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000006>

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791147588 1791147588 IN IP4 10.46.0.17
s=-
c=IN IP4 10.46.0.17
b=AS:41
b=RS:512
b=RR:1537
t=0 0
m=audio 49120 RTP/AVP 116 99 97 105 100
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:116 EVS/16000
a=fmtp:116 br=13.2; bw=nb-wb; max-red=220
a=rtpmap:99 AMR-WB/16000
a=fmtp:99 max-red=0; mode-change-capability=2
a=rtpmap:97 AMR/8000
a=fmtp:97 max-red=0; mode-change-capability=2
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=rtpmap:100 telephone-event/8000
a=fmtp:100 0-15
a=ptime:20
a=maxptime:240
a=inactive
a=des:qos mandatory local sendrecv
a=curr:qos local none
a=des:qos optional remote sendrecv
a=curr:qos remote none
