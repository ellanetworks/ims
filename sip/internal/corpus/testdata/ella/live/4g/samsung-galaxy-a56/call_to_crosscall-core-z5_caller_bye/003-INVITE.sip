INVITE sip:8476f6db-5c6a-4256-a0dd-69db67bd85bd@10.46.0.10:44417 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKAW4R73PBWIBM3KEVJS6FJWHBWL
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKGHIB27E2L2YSZ7WSJUP3MKFLTP
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKXEKOEPFHX5ZAWREVKWQ365VEQS
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKVLPYPTXTA2SUYDM2TVQJHJ2UES
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKIVFVWQTFAQ27ZMT5PJHHSBFIQC
Via: SIP/2.0/TCP 10.46.0.5:6100;branch=z9hG4bK-524287-1---4dcc4e82d7f2352b;rport=6101;transport=TCP;received=10.46.0.5
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5063;transport=tcp;lr;r2=on;did=BVHJA3NCOISW3VSJ;ue>
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5060;lr;r2=on;did=BVHJA3NCOISW3VSJ>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=AXKXF3F4BJY7PTUM>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=5TPKOWGUUW6Y4JQP>
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5060;lr;r2=on;did=RVC3TBNKMWVETGSR>
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5063;transport=tcp;lr;r2=on;did=RVC3TBNKMWVETGSR;ue>
Max-Forwards: 65
Contact: <sip:15550000007@10.46.0.5:6100>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone>
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=579fff3c
Call-ID: idFuruxAN21AThXOdDJ0zQ..@10.46.0.5
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
o=SAMSUNG-IMS-UE 91786296927 0 IN IP4 10.46.0.5
s=SS VOIP
c=IN IP4 10.46.0.5
t=0 0
m=audio 1238 RTP/AVP 112 116 107 118 96 111 110
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
