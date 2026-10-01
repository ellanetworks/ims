INVITE sip:0398765432100;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone SIP/2.0
From: <tel:0598765432100>;tag=4128004116
To: <sip:0398765432100;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>
CSeq: 906778637 INVITE
Call-ID: 4128004109_45009256@192.168.101.2
Via: SIP/2.0/TCP 192.168.101.2:5060;branch=z9hG4bK1185684359
Max-Forwards: 70
Contact: <sip:192.168.101.2:5060>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";video;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Route: <sip:10.4.128.21:5060;lr>,<sip:orig@scscf.ims.mnc001.mcc001.3gppnetwork.org:6060;lr>
P-Access-Network-Info: 3GPP-E-UTRAN-FDD; utran-cell-id-3gpp=001010001000019B
P-Preferred-Identity: <tel:0598765432100>
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
P-Preferred-Service: urn:urn-7:3gpp-service.ims.icsi.mmtel
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Supported: timer,100rel,replaces,histinfo,tdialog
P-Early-Media: supported
Content-Length: 469
Session-Expires: 1800;refresher=uac

v=0
o=- 1063 1000 IN IP4 192.168.101.2
s=QC VOIP
c=IN IP4 192.168.101.2
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50056 RTP/AVP 104 102 96 97
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:102 AMR/8000/1
a=fmtp:102 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=rtpmap:97 telephone-event/8000
a=fmtp:97 0-15
a=sendrecv
a=maxptime:240
a=ptime:20
