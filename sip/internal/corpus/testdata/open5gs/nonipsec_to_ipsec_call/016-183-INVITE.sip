SIP/2.0 183 Session Progress
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK871a.79c06bcb4044312db43553666beb849f.0
Via: SIP/2.0/UDP 10.4.128.21;branch=z9hG4bK871a.2e2ff827f034d8034b71c49d100eba8c.0;i=3
Via: SIP/2.0/TCP 192.168.101.2:5060;rport=43551;branch=z9hG4bK1185684359
Max-Forwards: 70
Record-Route: <sip:mt@10.4.128.21:5101;r2=on;lr=on;ftag=4128004116;rm=7;did=a22.d192>
Record-Route: <sip:mt@10.4.128.21;r2=on;lr=on;ftag=4128004116;rm=7;did=a22.d192>
Record-Route: <sip:mt@10.4.128.21:6060;lr=on;ftag=4128004116;did=a22.9692>
Record-Route: <sip:mo@10.4.128.21:6060;lr=on;ftag=4128004116;did=a22.8692>
Record-Route: <sip:mo@10.4.128.21;r2=on;lr=on;ftag=4128004116;rm=8;did=a22.c192>
Record-Route: <sip:mo@10.4.128.21;transport=tcp;r2=on;lr=on;ftag=4128004116;rm=8;did=a22.c192>
Require: 100rel
Contact: <sip:0398765432100@192.168.101.4:6200;alias=192.168.101.4~6201~2>;+sip.instance="<urn:gsma:imei:35622410-483840-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <sip:0398765432100;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=622eae5e
From: <tel:0598765432100>;tag=4128004116
Call-ID: 4128004109_45009256@192.168.101.2
CSeq: 906778637 INVITE
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel
User-Agent: SM-T515-TB4 Samsung IMS 6.0
RSeq: 1
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001000019b
Content-Length: 335
C-Params: +sip.instance="<urn:gsma:imei:35622410-483840-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting

v=0
o=SAMSUNG-IMS-UE 336137736 0 IN IP4 10.4.128.21
s=SS VOIP
c=IN IP4 10.4.128.21
t=0 0
m=audio 30022 RTP/AVP 104 96
b=AS:41
b=RS:512
b=RR:1537
a=maxptime:240
a=rtpmap:104 AMR-WB/16000/1
a=rtpmap:96 telephone-event/16000
a=fmtp:104 mode-change-capability=2;max-red=0
a=fmtp:96 0-15
a=sendrecv
a=rtcp:30023
a=ptime:20
