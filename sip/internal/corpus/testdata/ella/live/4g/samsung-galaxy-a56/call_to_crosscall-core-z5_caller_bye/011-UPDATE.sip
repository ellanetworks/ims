UPDATE sip:8476f6db-5c6a-4256-a0dd-69db67bd85bd@10.46.0.10:44417 SIP/2.0
Via: SIP/2.0/TCP 10.46.0.5:6100;branch=z9hG4bK-524287-1---6e6e5e7a2cf5140d;rport;transport=TCP
Max-Forwards: 70
Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5063;transport=tcp;lr;r2=on;did=RVC3TBNKMWVETGSR;ue>
Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5060;lr;r2=on;did=RVC3TBNKMWVETGSR>
Route: <sip:mo@10.80.0.5:5080;lr;did=5TPKOWGUUW6Y4JQP>
Route: <sip:mt@10.80.0.5:5080;lr;did=AXKXF3F4BJY7PTUM>
Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5060;lr;r2=on;did=BVHJA3NCOISW3VSJ>
Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5063;transport=tcp;lr;r2=on;did=BVHJA3NCOISW3VSJ;ue>
Proxy-Require: sec-agree
Require: precondition, sec-agree
Contact: <sip:15550000007@10.46.0.5:6100>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone>;tag=5939484
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=579fff3c
Call-ID: idFuruxAN21AThXOdDJ0zQ..@10.46.0.5
CSeq: 3 UPDATE
Session-Expires: 1800;refresher=uac
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition, sec-agree
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=132102118;spi-s=104856785;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
P-Preferred-Identity: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=9990100010019b01
Content-Length: 445

v=0
o=SAMSUNG-IMS-UE 91786296927 1 IN IP4 10.46.0.5
s=SS VOIP
c=IN IP4 10.46.0.5
t=0 0
m=audio 1238 RTP/AVP 116 111
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
