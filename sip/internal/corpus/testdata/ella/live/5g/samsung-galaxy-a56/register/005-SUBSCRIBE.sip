SUBSCRIBE sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org SIP/2.0
Via: SIP/2.0/UDP 10.46.0.2:6400;branch=z9hG4bK-524287-1---204333ba4617c51f;rport;transport=UDP
Max-Forwards: 70
Route: <sip:10.80.0.5:5063;lr>
Route: <sip:orig-68@scscf.ims.mnc001.mcc999.3gppnetwork.org:5080;lr>
Proxy-Require: sec-agree
Require: sec-agree
Contact: <sip:15550000007@10.46.0.2:6400>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.smsip
To: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>
From: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=6a038016
Call-ID: am-R1q6C4oM-Zpsmbic-_w..@10.46.0.2
CSeq: 1 SUBSCRIBE
Expires: 600000
Accept: application/reginfo+xml
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Supported: sec-agree
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
Event: reg
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=30560604;spi-s=41880514;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
P-Preferred-Identity: <sip:15550000007@ims.mnc001.mcc999.3gppnetwork.org>
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=9990100000100066c000
Content-Length: 0

