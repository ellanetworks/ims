INVITE sip:a87a2e9a-4c32-439f-afc0-825ea2f8b236@10.46.0.9:43885 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKQSPS7PAX3TZJAIWZDRGFEHMKGP
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKNKBYB2UQGFZ5TSAA7HZ627CXMJ
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK3C2Y35OZXJ26RTHXSYQSHS4WHD
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKSDXCNOUZTW3SZ6SSOKQ3X5QPHL
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKBXUP2B2ZZ4MBKM6KSUQ2KMHXKG
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=111319409
To: <tel:15550000008;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
CSeq: 111319402 INVITE
Call-ID: 111319402_1761473040@10.46.0.8
Via: SIP/2.0/TCP 10.46.0.8:42225;branch=z9hG4bK735318805
Record-Route: <sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5063;transport=tcp;lr;r2=on;did=KEIQ7AUMVIAEI7KE;ue>
Record-Route: <sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5060;lr;r2=on;did=KEIQ7AUMVIAEI7KE>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=XFM6GZDIASPZJWPX>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=4BHLROODLRCP3SS5>
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5060;lr;r2=on;did=6ELVBEC7G6227QEB>
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5063;transport=tcp;lr;r2=on;did=6ELVBEC7G6227QEB;ue>
Max-Forwards: 65
Contact: <sip:2da86196-c8f7-4939-af8b-fbdac4e983f2@10.46.0.8:42225>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Supported: timer,199,100rel,replaces,precondition,from-change,histinfo,tdialog
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Length: 762
Session-Expires: 1800;refresher=uac
P-Called-Party-ID: <tel:+15550000008>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000005>

v=0
o=- 21 1000 IN IP4 10.46.0.8
s=QC VOIP
c=IN IP4 10.46.0.8
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50054 RTP/AVP 104 110 102 108 96 97
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:110 AMR-WB/16000/1
a=fmtp:110 octet-align=1;mode-change-capability=2;max-red=0
a=rtpmap:102 AMR/8000/1
a=fmtp:102 mode-change-capability=2;max-red=0
a=rtpmap:108 AMR/8000/1
a=fmtp:108 octet-align=1;mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=rtpmap:97 telephone-event/8000
a=fmtp:97 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
