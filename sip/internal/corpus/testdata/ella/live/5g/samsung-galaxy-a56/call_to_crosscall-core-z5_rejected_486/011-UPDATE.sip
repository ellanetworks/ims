UPDATE sip:cbcd92dc-db6f-4aea-a207-e26b95efb9f2@10.46.0.8:41989 SIP/2.0
Via: SIP/2.0/TCP 10.46.0.7:6600;branch=z9hG4bK-524287-1---049c32b76d32c497;rport;transport=TCP
Max-Forwards: 70
Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5063;transport=tcp;lr;r2=on;did=W4LQBCOD7YORI5O7;ue>
Route: <sip:3iqp554vb5dijnjmjo2u3xbt2t@10.80.0.5:5060;lr;r2=on;did=W4LQBCOD7YORI5O7>
Route: <sip:mo@10.80.0.5:5080;lr;did=PRWHK5F2LTQVRP77>
Route: <sip:mt@10.80.0.5:5080;lr;did=LMFV3M434C4WGM5L>
Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5060;lr;r2=on;did=2AXOPYHCFZJCPDGI>
Route: <sip:75xi5p5r4lmyujqtijnkhr24d7@10.80.0.5:5063;transport=tcp;lr;r2=on;did=2AXOPYHCFZJCPDGI;ue>
Proxy-Require: sec-agree
Require: precondition, sec-agree
Contact: <sip:15550000007@10.46.0.7:6600>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
To: <sip:15550000008;phone-context=ims.mnc001.mcc999.3gppnetwork.org@ims.mnc001.mcc999.3gppnetwork.org;user=phone>;tag=100223009
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=14031060
Call-ID: o9voWSJ5T_Wr2ef6EsB2vg..@10.46.0.7
CSeq: 3 UPDATE
Session-Expires: 1800;refresher=uac
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Content-Type: application/sdp
Supported: timer, 100rel, precondition, sec-agree
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=167591435;spi-s=140386225;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
P-Preferred-Identity: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=9990100000100066c000
Content-Length: 446

v=0
o=SAMSUNG-IMS-UE 186072712835 1 IN IP4 10.46.0.7
s=SS VOIP
c=IN IP4 10.46.0.7
t=0 0
m=audio 1246 RTP/AVP 116 111
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
