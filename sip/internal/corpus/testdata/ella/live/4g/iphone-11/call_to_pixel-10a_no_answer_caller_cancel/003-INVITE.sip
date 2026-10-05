INVITE sip:029e6db2-2b39-9439-8415-24e0ec03c97a@10.46.0.24:49165 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKLQK6BNWQFGOMZML73EW6X6O4Z2
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK6YJ5CRGTUWR72JNBOLQVKWP7AH
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKNCVS53CI7LMEDGLS5WYPP57JRS
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKVHQAHDKBVFVWB4KMXFE5GMGJ4X
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK5VVFVVJRZJMAMY4RT3TATNJXRX
Accept: application/3gpp-ims+xml,application/sdp
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Supported: 100rel,path,precondition,replaces,timer
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=N9VFxKYIi7
To: <tel:+15550000002>
Call-ID: Vp5lvr2fZa3rj8x005nYYEPM
Session-ID: 81ea83e819dd815b4cc760f421299969
Contact: <sip:10.46.0.25:63507>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
CSeq: 1 INVITE
Via: SIP/2.0/TCP 10.46.0.25:63507;branch=z9hG4bKpJ8Du7v3SmDc1Cp;rport=64105;received=10.46.0.25
Record-Route: <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5063;transport=tcp;lr;r2=on;ob;did=5FM5CGBUFV4OODSU;ue>
Record-Route: <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5060;lr;r2=on;ob;did=5FM5CGBUFV4OODSU>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=NW5S4KKIPFQWPQUA>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=KACYVEHWOAQWQUXS>
Record-Route: <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5060;lr;r2=on;did=HWFAUINCWZVHSK4D>
Record-Route: <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5063;transport=tcp;lr;r2=on;did=HWFAUINCWZVHSK4D;ue>
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
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791203043 1791203043 IN IP4 10.46.0.25
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
