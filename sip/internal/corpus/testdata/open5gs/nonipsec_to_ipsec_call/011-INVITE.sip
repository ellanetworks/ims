INVITE sip:192.168.101.4:6200 SIP/2.0
Record-Route: <sip:mt@10.4.128.21:5101;r2=on;lr=on;ftag=4128004116;rm=7;did=a22.d192>
Record-Route: <sip:mt@10.4.128.21;r2=on;lr=on;ftag=4128004116;rm=7;did=a22.d192>
Record-Route: <sip:mt@10.4.128.21:6060;lr=on;ftag=4128004116;did=a22.9692>
Record-Route: <sip:mo@10.4.128.21:6060;lr=on;ftag=4128004116;did=a22.8692>
Record-Route: <sip:mo@10.4.128.21;r2=on;lr=on;ftag=4128004116;rm=8;did=a22.c192>
Record-Route: <sip:mo@10.4.128.21;transport=tcp;r2=on;lr=on;ftag=4128004116;rm=8;did=a22.c192>
From: <tel:0598765432100>;tag=4128004116
To: <sip:0398765432100;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>
CSeq: 906778637 INVITE
Call-ID: 4128004109_45009256@192.168.101.2
Via: SIP/2.0/UDP 10.4.128.21:5101;branch=z9hG4bK871a.a5cd3a8d2d34670b173e76a6bab6d24e.0
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK871a.bbc9d5d400ee2e0a9b36c4f10640d958.0
Via: SIP/2.0/UDP 10.4.128.21:4060;branch=z9hG4bK871a.ee737663fa704bc202c40661664fb603.1
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK871a.79c06bcb4044312db43553666beb849f.0
Via: SIP/2.0/UDP 10.4.128.21;branch=z9hG4bK871a.2e2ff827f034d8034b71c49d100eba8c.0;i=3
Via: SIP/2.0/TCP 192.168.101.2:5060;rport=43551;branch=z9hG4bK1185684359
Max-Forwards: 65
Contact: <sip:192.168.101.2:5060;alias=192.168.101.2~43551~2>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";video;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
P-Access-Network-Info: 3GPP-E-UTRAN-FDD; utran-cell-id-3gpp=001010001000019B
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
P-Preferred-Service: urn:urn-7:3gpp-service.ims.icsi.mmtel
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Supported: timer,100rel,replaces,histinfo,tdialog
P-Early-Media: supported
Content-Length: 479
Session-Expires: 1800;refresher=uac
P-Charging-Vector: icid-value=4956537F000001BB6100003C00000000; icid-generated-at=10.4.128.21
P-Visited-Network-ID: ims.mnc001.mcc001.3gppnetwork.org
P-Asserted-Identity: <sip:0598765432100@<null>>
Supported: sec-agree
Security-Server: ipsec-3gpp;prot=esp;mod=trans;spi-c=102;spi-s=103;port-c=5101;port-s=6101;alg=hmac-md5-96;ealg=null
Supported: sec-agree
Security-Server: ipsec-3gpp;prot=esp;mod=trans;spi-c=102;spi-s=103;port-c=5101;port-s=6101;alg=hmac-md5-96;ealg=null

v=0
o=- 1063 1000 IN IP4 10.4.128.21
s=QC VOIP
c=IN IP4 10.4.128.21
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 30000 RTP/AVP 104 102 96 97
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
a=rtcp:30001
a=ptime:20
