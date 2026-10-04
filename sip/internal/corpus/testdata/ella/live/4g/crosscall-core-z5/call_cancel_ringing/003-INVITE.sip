INVITE sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKCGJ4NLYIB2JPMS6IGOHFLXJK2R
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKKPNZHR5UFYL4ANAV4KZQ7WXAJ2
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKXUAPLQHH4YBCMH4UMUPG4RRKJV
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKCN6LHELDWASKR2YQYNLZYX3CNI
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK42DJFBZGOUFHN54PWRGMWSS66L
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=354254
To: <tel:15550000004;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
CSeq: 354250 INVITE
Call-ID: 354250_1760822368@10.46.0.13
Via: SIP/2.0/TCP 10.46.0.13:40279;branch=z9hG4bK971109358
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=LH2F52M7MD4EXCGO;ue>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=LH2F52M7MD4EXCGO>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=UQC6CAJAE5RADFSU>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=ZORNBAWCUPERX3S5>
Record-Route: <sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5060;lr;r2=on;did=OC466DP3NUUY5YWK>
Record-Route: <sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5063;transport=tcp;lr;r2=on;did=OC466DP3NUUY5YWK;ue>
Max-Forwards: 65
Contact: <sip:6360b6ba-8a1f-493d-a05a-f4121465d17e@10.46.0.13:40279>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Supported: timer,199,100rel,replaces,precondition,from-change,histinfo,tdialog
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Length: 763
Session-Expires: 1800;refresher=uac
P-Called-Party-ID: <tel:+15550000004>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000005>

v=0
o=- 6 1000 IN IP4 10.46.0.13
s=QC VOIP
c=IN IP4 10.46.0.13
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50032 RTP/AVP 104 110 102 108 96 97
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
