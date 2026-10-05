INVITE sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone SIP/2.0
Via: SIP/2.0/TCP 10.46.0.5:6100;branch=z9hG4bK-524287-1---f4139a6a9096ff6d;rport;transport=TCP
Max-Forwards: 70
Route: <sip:10.80.0.5:5063;lr>
Route: <sip:orig-51@scscf.ims.mnc001.mcc999.3gppnetwork.org:5080;lr>
Proxy-Require: sec-agree
Require: sec-agree
Contact: <sip:15550000007@10.46.0.5:6100>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone>
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=d9d80d7e
Call-ID: pFnJ7GJsA1lFS6RnnqR7OQ..@10.46.0.5
CSeq: 1 INVITE
Session-Expires: 1800
Accept: application/sdp, application/3gpp-ims+xml
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition, gruu, sec-agree
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=132102118;spi-s=104856785;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
P-Preferred-Identity: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
P-Early-Media: supported
P-Preferred-Service: urn:urn-7:3gpp-service.ims.icsi.mmtel
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=9990100010019b01
Content-Length: 869

v=0
o=SAMSUNG-IMS-UE 91957763312 0 IN IP4 10.46.0.5
s=SS VOIP
c=IN IP4 10.46.0.5
t=0 0
m=audio 1240 RTP/AVP 112 116 107 118 96 111 110
b=AS:42
b=RS:0
b=RR:2500
a=rtpmap:112 EVS/16000
a=fmtp:112 br=5.9-24.4;bw=nb-swb;ch-aw-recv=-1
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=220
a=rtpmap:107 AMR-WB/16000/1
a=fmtp:107 octet-align=1;mode-change-capability=2;max-red=220
a=rtpmap:118 AMR/8000/1
a=fmtp:118 mode-set=0,2,4,7;mode-change-capability=2;max-red=220
a=rtpmap:96 AMR/8000/1
a=fmtp:96 mode-set=0,2,4,7;octet-align=1;mode-change-capability=2;max-red=220
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
