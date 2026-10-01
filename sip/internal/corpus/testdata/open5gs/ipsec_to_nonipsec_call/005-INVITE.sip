INVITE sip:0598765432100;phone-context=ims.mnc001.mcc262.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone SIP/2.0
Via: SIP/2.0/TCP 192.168.101.6:6700;branch=z9hG4bK-524287-1---e4d6b60a70aa2dc6;rport;transport=TCP
Max-Forwards: 70
Route: <sip:10.4.128.21:6106;lr>
Route: <sip:orig@scscf.ims.mnc001.mcc001.3gppnetwork.org:6060;lr>
Proxy-Require: sec-agree
Require: sec-agree
Contact: <sip:001011234567893@192.168.101.6:6700>;+sip.instance="<urn:gsma:imei:35622410-483840-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
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
Security-Verify: ipsec-3gpp;prot=esp;mod=trans;spi-c=112;spi-s=113;port-c=5106;port-s=6106;alg=hmac-sha-1-96;ealg=null
P-Preferred-Identity: <sip:001011234567893@ims.mnc001.mcc001.3gppnetwork.org>
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
P-Early-Media: supported
P-Preferred-Service: urn:urn-7:3gpp-service.ims.icsi.mmtel
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001000019b
Content-Length: 760

v=0
o=SAMSUNG-IMS-UE 2739144830 0 IN IP4 192.168.101.6
s=SS VOIP
c=IN IP4 192.168.101.6
t=0 0
m=audio 1244 RTP/AVP 116 107 118 96 111 110
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=0
a=rtpmap:107 AMR-WB/16000/1
a=fmtp:107 octet-align=1;mode-change-capability=2;max-red=0
a=rtpmap:118 AMR/8000/1
a=fmtp:118 mode-change-capability=2;max-red=0
a=rtpmap:96 AMR/8000/1
a=fmtp:96 octet-align=1;mode-change-capability=2;max-red=0
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=rtpmap:110 telephone-event/8000
a=fmtp:110 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
a=sendrecv
a=ptime:20
a=maxptime:240
