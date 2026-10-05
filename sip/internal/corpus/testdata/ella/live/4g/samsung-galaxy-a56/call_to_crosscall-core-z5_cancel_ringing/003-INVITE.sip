INVITE sip:8476f6db-5c6a-4256-a0dd-69db67bd85bd@10.46.0.10:44417 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKQNNOAOIA75FV3NWZBYT5LBXZ6D
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK4VFOMVTEONZ4O66NL4RTWPREH3
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKVVVHFXVIWOJK3EVBHLKVBMZWQ7
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKYKZH2FJCBWX4WL76BCP4GYZJIO
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKSE25V5FUV4QGAG62FWKHJMNZSU
Via: SIP/2.0/TCP 10.46.0.5:6100;branch=z9hG4bK-524287-1---308562a050de63c6;rport=6101;transport=TCP;received=10.46.0.5
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5063;transport=tcp;lr;r2=on;did=3UVFE7TGRX4IMJJ2;ue>
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5060;lr;r2=on;did=3UVFE7TGRX4IMJJ2>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=PVUCXWWT3NOWZVAR>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=PXZFFJC7HFBRHS6O>
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5060;lr;r2=on;did=WN2QGXYIM74URNZW>
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5063;transport=tcp;lr;r2=on;did=WN2QGXYIM74URNZW;ue>
Max-Forwards: 65
Contact: <sip:15550000007@10.46.0.5:6100>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone>
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=799bc610
Call-ID: M7fI0X_bWWJ8IkGXKd2qGg..@10.46.0.5
CSeq: 1 INVITE
Session-Expires: 1800
Accept: application/sdp, application/3gpp-ims+xml
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition, gruu, sec-agree
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Content-Length: 869
P-Called-Party-ID: <tel:+15550000008>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000007>

v=0
o=SAMSUNG-IMS-UE 92414499381 0 IN IP4 10.46.0.5
s=SS VOIP
c=IN IP4 10.46.0.5
t=0 0
m=audio 1248 RTP/AVP 112 116 107 118 96 111 110
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
