SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.46.0.10:44417;branch=z9hG4bK1545228194
Max-Forwards: 70
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5063;transport=tcp;lr;r2=on;did=MIMAOHYYMLGW3HWO;ue>
Record-Route: <sip:xecajs2762idoehm7nsyclb72l@10.80.0.5:5060;lr;r2=on;did=MIMAOHYYMLGW3HWO>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=PAJEAODRYRU4WCLV>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=WKFEAAXSOHIFOTX2>
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5060;lr;r2=on;did=6MZTY7B6SYMJA37E>
Record-Route: <sip:hgmaj5sms7v54nx74sze7jdv7u@10.80.0.5:5063;transport=tcp;lr;r2=on;did=6MZTY7B6SYMJA37E;ue>
Require: precondition
Contact: <sip:15550000007@10.46.0.5:6100>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <tel:15550000007;phone-context=ims.mnc001.mcc999.3gppnetwork.org>;tag=0542ad56
From: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=7022197
Call-ID: 7022189_3071011792@10.46.0.10
CSeq: 7022191 UPDATE
Session-Expires: 1800;refresher=uas
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
Content-Length: 446

v=0
o=SAMSUNG-IMS-UE 92871298151 1 IN IP4 10.46.0.5
s=SS VOIP
c=IN IP4 10.46.0.5
t=0 0
m=audio 1256 RTP/AVP 104 96
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
