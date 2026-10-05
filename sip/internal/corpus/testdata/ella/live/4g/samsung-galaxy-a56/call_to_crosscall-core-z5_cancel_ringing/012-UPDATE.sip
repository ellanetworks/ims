UPDATE sip:8476f6db-5c6a-4256-a0dd-69db67bd85bd@10.46.0.10:44417 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKQWVAHKJGM6J5SVLRGENM75GOA2
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKWR5MBAQKZ6FD2GULVPHEPGM6AT
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKHYBV56GCXHIO6V2PE636P2XOAD
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKXZIZZ6SP5YFUOKYFEMCQ22DUSY
Via: SIP/2.0/TCP 10.46.0.5:6100;branch=z9hG4bK-524287-1---ac675171f35656ad;rport=6101;transport=TCP;received=10.46.0.5
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5063;transport=tcp;lr;r2=on;did=3UVFE7TGRX4IMJJ2;ue>
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5060;lr;r2=on;did=3UVFE7TGRX4IMJJ2>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=PVUCXWWT3NOWZVAR>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=PXZFFJC7HFBRHS6O>
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5060;lr;r2=on;did=WN2QGXYIM74URNZW>
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5063;transport=tcp;lr;r2=on;did=WN2QGXYIM74URNZW;ue>
Max-Forwards: 66
Contact: <sip:15550000007@10.46.0.5:6100>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone>;tag=6567771
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=799bc610
Call-ID: M7fI0X_bWWJ8IkGXKd2qGg..@10.46.0.5
CSeq: 3 UPDATE
Session-Expires: 1800;refresher=uac
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition, sec-agree
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
Content-Length: 445
Require: precondition

v=0
o=SAMSUNG-IMS-UE 92414499381 1 IN IP4 10.46.0.5
s=SS VOIP
c=IN IP4 10.46.0.5
t=0 0
m=audio 1248 RTP/AVP 116 111
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=0
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=ptime:20
a=maxptime:240
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
