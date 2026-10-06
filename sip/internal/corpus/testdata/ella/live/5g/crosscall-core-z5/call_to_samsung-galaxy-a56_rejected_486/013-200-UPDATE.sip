SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKPNCUCSUHE7HRHVUVTI7BK5IYVB
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKP4KSKY5OGVXIAZ6ZOHGFERYOYJ
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKI33IQJSDY3A47CFTBKNMMAGTLT
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKJFMJQQQTUKL75N3XPJ5S4CNDTD
Via: SIP/2.0/TCP 10.46.0.8:41989;branch=z9hG4bK2102119903
Max-Forwards: 70
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5063;transport=tcp;lr;r2=on;did=FLUIUSQENRDFGD2K;ue>
Record-Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5060;lr;r2=on;did=FLUIUSQENRDFGD2K>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=Q6G6GKCZOK2FLXON>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=EVNTE6S5UQASFCPT>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5060;lr;r2=on;did=OFUBLLFGBYHHRK5W>
Record-Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=OFUBLLFGBYHHRK5W;ue>
Require: precondition
Contact: <sip:15550000007@10.46.0.7:6600>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <tel:15550000007;phone-context=ims.mnc001.mcc999.3gppnetwork.org>;tag=23426e4a
From: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=100699893
Call-ID: 100699886_3068622352@10.46.0.8
CSeq: 100699888 UPDATE
Session-Expires: 1800;refresher=uas
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=9990100000100066c000
Content-Length: 447

v=0
o=SAMSUNG-IMS-UE 186549954942 1 IN IP4 10.46.0.7
s=SS VOIP
c=IN IP4 10.46.0.7
t=0 0
m=audio 1252 RTP/AVP 104 96
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
