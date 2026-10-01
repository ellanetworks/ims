INVITE sip:192.168.101.2:5060;alias=192.168.101.2~5060~1 SIP/2.0
Record-Route: <sip:mt@10.4.128.21:6060;lr=on;ftag=401181971>
Route: <sip:term@pcscf.ims.mnc001.mcc001.3gppnetwork.org;lr>
Record-Route: <sip:mo@10.4.128.21:6060;lr=on;ftag=401181971>
Record-Route: <sip:mo@10.4.128.21;r2=on;lr=on;ftag=401181971;rm=8;did=a78.1bf>
Record-Route: <sip:mo@10.4.128.21;transport=tcp;r2=on;lr=on;ftag=401181971;rm=8;did=a78.1bf>
From: <tel:0298765432100>;tag=401181971
To: <sip:0198765432100;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>
CSeq: 401181964 INVITE
Call-ID: 401181964_495972856@192.168.101.3
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bKdc9b.76e25db1575b65ab30dfdd55c1b15605.0
Via: SIP/2.0/UDP 10.4.128.21:4060;branch=z9hG4bKdc9b.315a40ad467066edbddb863f62d8e519.1
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bKdc9b.70352076781b298a6b85eca4885a8312.0
Via: SIP/2.0/UDP 10.4.128.21;branch=z9hG4bKdc9b.6e71f5c2099bf99d50b39959dd326117.0;i=1
Via: SIP/2.0/TCP 192.168.101.3:5060;rport=40762;branch=z9hG4bK559617467
Max-Forwards: 66
Contact: <sip:192.168.101.3:5060;alias=192.168.101.3~40762~2>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
P-Access-Network-Info: 3GPP-E-UTRAN-FDD; utran-cell-id-3gpp=001010001000019C
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
P-Preferred-Service: urn:urn-7:3gpp-service.ims.icsi.mmtel
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Supported: timer,100rel,replaces,histinfo,tdialog
P-Early-Media: supported
Content-Length: 479
Session-Expires: 1800;refresher=uac
P-Charging-Vector: icid-value=4956537F000001BE6900001F00000000; icid-generated-at=10.4.128.21
P-Visited-Network-ID: ims.mnc001.mcc001.3gppnetwork.org
P-Asserted-Identity: <sip:0298765432100@<null>>

v=0
o=- 1079 1000 IN IP4 10.4.128.21
s=QC VOIP
c=IN IP4 10.4.128.21
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 31226 RTP/AVP 104 102 96 97
b=AS:41
b=RS:600
b=RR:2000
a=maxptime:240
a=rtpmap:104 AMR-WB/16000/1
a=rtpmap:102 AMR/8000/1
a=rtpmap:96 telephone-event/16000
a=rtpmap:97 telephone-event/8000
a=fmtp:104 mode-change-capability=2;max-red=0
a=fmtp:102 mode-change-capability=2;max-red=0
a=fmtp:96 0-15
a=fmtp:97 0-15
a=sendrecv
a=rtcp:31227
a=ptime:20
