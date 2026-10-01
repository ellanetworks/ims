INVITE sip:192.168.101.7:5060 SIP/2.0
Record-Route: <sip:mt@10.4.128.21;lr=on;ftag=b29e8e10;rm=7;did=0f5.4771>
Record-Route: <sip:mt@10.4.128.21:6060;lr=on;ftag=b29e8e10;did=0f5.ffa1>
Record-Route: <sip:mo@10.4.128.21:6060;lr=on;ftag=b29e8e10;did=0f5.efa1>
Record-Route: <sip:mo@10.4.128.21;r2=on;lr=on;ftag=b29e8e10;rm=8;did=0f5.3771>
Record-Route: <sip:mo@10.4.128.21:6106;transport=tcp;r2=on;lr=on;ftag=b29e8e10;rm=8;did=0f5.3771>
Via: SIP/2.0/UDP 10.4.128.21;branch=z9hG4bK826f.363a6c144c7710901cf44711a90bdddd.0
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK826f.08a2db7e44e7f758cbb0f50c7a4fff12.0
Via: SIP/2.0/UDP 10.4.128.21:4060;branch=z9hG4bK826f.616718c9c228c06ad6dc0a1ce2ba2456.1
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK826f.7877e5600d25e4defa00a55f10c2870b.0
Via: SIP/2.0/UDP 10.4.128.21;branch=z9hG4bK826f.57bcd4f8583db0807b4a9aece35f87a6.0;i=c
Via: SIP/2.0/TCP 192.168.101.6:6700;received=192.168.101.6;branch=z9hG4bK-524287-1---e4d6b60a70aa2dc6;rport=6701;transport=TCP
Max-Forwards: 65
Contact: <sip:001011234567893@192.168.101.6:6700;alias=192.168.101.6~6701~2>;+sip.instance="<urn:gsma:imei:35622410-483840-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <sip:0598765432100;phone-context=ims.mnc001.mcc262.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>
From: <sip:001011234567893@ims.mnc001.mcc001.3gppnetwork.org>;tag=b29e8e10
Call-ID: UeE-Kc-iNlv7QvhZbppI2Q..@192.168.101.6
CSeq: 1 INVITE
Session-Expires: 1800
Accept: application/sdp, application/3gpp-ims+xml
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition, gruu, histinfo, sec-agree
User-Agent: SM-T515-XXS4ATB4 Samsung IMS 6.0 DBT
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
P-Early-Media: supported
P-Preferred-Service: urn:urn-7:3gpp-service.ims.icsi.mmtel
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001000019b
Content-Length: 771
P-Charging-Vector: icid-value=4956537F00000169690000E801000000; icid-generated-at=10.4.128.21
P-Visited-Network-ID: ims.mnc001.mcc001.3gppnetwork.org
P-Asserted-Identity: <sip:001011234567893@ims.mnc001.mcc001.3gppnetwork.org>

v=0
o=SAMSUNG-IMS-UE 2739144830 0 IN IP4 10.4.128.21
s=SS VOIP
c=IN IP4 10.4.128.21
t=0 0
m=audio 30328 RTP/AVP 116 107 118 96 111 110
b=AS:41
b=RS:512
b=RR:1537
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
a=maxptime:240
a=rtpmap:116 AMR-WB/16000/1
a=rtpmap:107 AMR-WB/16000/1
a=rtpmap:118 AMR/8000/1
a=rtpmap:96 AMR/8000/1
a=rtpmap:111 telephone-event/16000
a=rtpmap:110 telephone-event/8000
a=fmtp:116 mode-change-capability=2;max-red=0
a=fmtp:107 octet-align=1;mode-change-capability=2;max-red=0
a=fmtp:118 mode-change-capability=2;max-red=0
a=fmtp:96 octet-align=1;mode-change-capability=2;max-red=0
a=fmtp:111 0-15
a=fmtp:110 0-15
a=sendrecv
a=rtcp:30329
a=ptime:20
