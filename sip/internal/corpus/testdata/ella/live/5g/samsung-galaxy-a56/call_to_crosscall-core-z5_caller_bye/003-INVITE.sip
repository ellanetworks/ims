INVITE sip:3b7d83d2-0108-4629-a2dd-0f4c6e59a52d@10.46.0.6:40117 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKJNJGLCWSSNVOFEVHULRBMJHBI6
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKRMEGK74KMJQNT6FSJRFUS2JZZS
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKBT2CM2XBJBC5KUCJ3AMGK4PUY4
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKBDTJS6OGJYV5FDUDWSBGZ4QKIV
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKMRWABTOJ5JJXQ76N7GKZV2REDZ
Via: SIP/2.0/TCP 10.46.0.5:6500;branch=z9hG4bK-524287-1---77ed7d620fb58e77;rport=6501;transport=TCP;received=10.46.0.5
Record-Route: <sip:a4jyxqxxrkgzk5gcxwc37cj5mu@10.80.0.5:5063;transport=tcp;lr;r2=on;did=JZKNSMCQQUGFEAX2;ue>
Record-Route: <sip:a4jyxqxxrkgzk5gcxwc37cj5mu@10.80.0.5:5060;lr;r2=on;did=JZKNSMCQQUGFEAX2>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=EHQA36QRI2KJR6G6>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=SFRRWN55URLYEDVY>
Record-Route: <sip:yuvey7vgymc2aajptku4giz3c7@10.80.0.5:5060;lr;r2=on;did=RTEXECWX4JG72SOL>
Record-Route: <sip:yuvey7vgymc2aajptku4giz3c7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=RTEXECWX4JG72SOL;ue>
Max-Forwards: 65
Contact: <sip:15550000007@10.46.0.5:6500>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone>
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=f760af74
Call-ID: UABUq-3ZLXm8QvgUlGitfg..@10.46.0.5
CSeq: 1 INVITE
Session-Expires: 1800
Accept: application/sdp, application/3gpp-ims+xml
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition, gruu, sec-agree
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
Content-Length: 870
P-Called-Party-ID: <tel:+15550000008>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000007>

v=0
o=SAMSUNG-IMS-UE 184014976248 0 IN IP4 10.46.0.5
s=SS VOIP
c=IN IP4 10.46.0.5
t=0 0
m=audio 1234 RTP/AVP 112 116 107 118 96 111 110
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
