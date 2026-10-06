UPDATE sip:3b7d83d2-0108-4629-a2dd-0f4c6e59a52d@10.46.0.6:40117 SIP/2.0
Via: SIP/2.0/TCP 10.46.0.5:6500;branch=z9hG4bK-524287-1---6613df788636fb8a;rport;transport=TCP
Max-Forwards: 70
Route: <sip:yuvey7vgymc2aajptku4giz3c7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=BGW57IAEJAAWUT2W;ue>
Route: <sip:yuvey7vgymc2aajptku4giz3c7@10.80.0.5:5060;lr;r2=on;did=BGW57IAEJAAWUT2W>
Route: <sip:mo@10.80.0.5:5080;lr;did=YLRXN2OJ2SHUOG4S>
Route: <sip:mt@10.80.0.5:5080;lr;did=ECKPY734NSIBAK5Q>
Route: <sip:a4jyxqxxrkgzk5gcxwc37cj5mu@10.80.0.5:5060;lr;r2=on;did=YKQTOSJSBY5MM6SY>
Route: <sip:a4jyxqxxrkgzk5gcxwc37cj5mu@10.80.0.5:5063;transport=tcp;lr;r2=on;did=YKQTOSJSBY5MM6SY;ue>
Proxy-Require: sec-agree
Require: precondition, sec-agree
Contact: <sip:15550000007@10.46.0.5:6500>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone>;tag=99052249
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=551cef53
Call-ID: cq2bdNGeGUuZz-QZvV6utw..@10.46.0.5
CSeq: 3 UPDATE
Session-Expires: 1800;refresher=uac
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition, sec-agree
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=32747763;spi-s=218694833;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
P-Preferred-Identity: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=9990100000100066c000
Content-Length: 446

v=0
o=SAMSUNG-IMS-UE 184902140515 1 IN IP4 10.46.0.5
s=SS VOIP
c=IN IP4 10.46.0.5
t=0 0
m=audio 1236 RTP/AVP 116 111
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
