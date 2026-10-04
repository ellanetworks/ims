UPDATE sip:10.46.0.17:61189 SIP/2.0
From: <sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org>;tag=1927058
To: <sip:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=zFJEGVPSyo
CSeq: 1927055 UPDATE
Call-ID: 1927053_4111230880@10.46.0.14
Content-Length: 451
Via: SIP/2.0/TCP 10.46.0.14:40265;branch=z9hG4bK3984370303
Max-Forwards: 70
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015EE0000
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=165898036;spi-s=190332794;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
Route: <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;did=YVJ2GKBMFEHHVT57;lr;r2=on;transport=tcp;ue>,<sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;did=YVJ2GKBMFEHHVT57;lr;r2=on>,<sip:mo@10.80.0.5:5080;did=6LG66HU6EAC2ZD2C;lr>,<sip:mt@10.80.0.5:5080;did=CAVDO2FD46F6RGJT;lr>,<sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=URACXRURYM4GEOMA;lr;r2=on>,<sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=URACXRURYM4GEOMA;lr;r2=on;transport=tcp;ue>
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: PRD-IR92/12 term-Motorola/XT2417-1-V1UFN35H.193-20 device-type/smart-phone
Content-Type: application/sdp
Require: sec-agree,precondition
Proxy-Require: sec-agree
Contact: <sip:59ca0608-182c-4e87-aed8-aa1e2a898c87@10.46.0.14:40265>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi

v=0
o=- 8 1001 IN IP4 10.46.0.14
s=QC VOIP
c=IN IP4 10.46.0.14
b=AS:42
b=RS:0
b=RR:0
t=0 0
m=audio 50016 RTP/AVP 106 96
b=AS:42
b=RS:0
b=RR:0
a=rtpmap:106 EVS/16000/1
a=fmtp:106 br=5.9-24.4;bw=nb-wb;ch-aw-recv=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
