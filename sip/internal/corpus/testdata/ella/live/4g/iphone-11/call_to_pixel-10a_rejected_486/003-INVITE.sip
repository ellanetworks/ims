INVITE sip:029e6db2-2b39-9439-8415-24e0ec03c97a@10.46.0.24:49165 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKDISUITVTNX47644VN572JCBNC7
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK5PJ7GTOL3NDM7R3P4RXF7JJGKZ
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKH3DB7URCRUZJGZBQWEGHFTUGFO
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKSJAJQYO3E25JHOR2KEPWSM34UN
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK3HDEXKFK37WGLMOOUVAEBWAREF
Accept: application/3gpp-ims+xml,application/sdp
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Supported: 100rel,path,precondition,replaces,timer
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=EOBHXAMMB2
To: <tel:+15550000002>
Call-ID: qfbVCVt6QJ54MV64hnH5NzAf
Session-ID: 184f160cbf8bcce60fb030aaf370cb20
Contact: <sip:10.46.0.25:63507>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
CSeq: 1 INVITE
Via: SIP/2.0/TCP 10.46.0.25:63507;branch=z9hG4bKBxL22BDuuUvZ2YY;rport=64105;received=10.46.0.25
Record-Route: <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5063;transport=tcp;lr;r2=on;ob;did=QVL5T7W4UAKQEF5C;ue>
Record-Route: <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5060;lr;r2=on;ob;did=QVL5T7W4UAKQEF5C>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=2F5U5JWSV44XQ5KE>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=A6WQHUKUTBVHHHJN>
Record-Route: <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5060;lr;r2=on;did=CPNLDKUQZRYHEPCG>
Record-Route: <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5063;transport=tcp;lr;r2=on;did=CPNLDKUQZRYHEPCG;ue>
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 65
User-Agent: iOS/27.0 iPhone
Content-Type: application/sdp
Content-Length: 705
P-Called-Party-ID: <tel:+15550000002>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000006>

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791202975 1791202975 IN IP4 10.46.0.25
s=-
c=IN IP4 10.46.0.25
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
