ACK sip:192.168.101.8:5060;alias=192.168.101.8~5060~1 SIP/2.0
Via: SIP/2.0/UDP 192.168.101.6:6700;branch=z9hG4bK-524287-1---c19065c5c5329ef9;rport;transport=UDP
Max-Forwards: 70
Route: <sip:mo@10.4.128.21:6106;transport=tcp;r2=on;lr=on;ftag=b29e8e10;rm=8;did=0f5.3771>
Route: <sip:mo@10.4.128.21;r2=on;lr=on;ftag=b29e8e10;rm=8;did=0f5.3771>
Route: <sip:mo@10.4.128.21:6060;lr=on;ftag=b29e8e10;did=0f5.efa1>
Route: <sip:mt@10.4.128.21:6060;lr=on;ftag=b29e8e10;did=0f5.ffa1>
Route: <sip:mt@10.4.128.21;lr=on;ftag=b29e8e10;rm=7;did=0f5.5771>
Proxy-Require: sec-agree
Require: sec-agree
To: <sip:0598765432100;phone-context=ims.mnc001.mcc262.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=4133807704
From: <sip:001011234567893@ims.mnc001.mcc001.3gppnetwork.org>;tag=b29e8e10
Call-ID: UeE-Kc-iNlv7QvhZbppI2Q..@192.168.101.6
CSeq: 1 ACK
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Supported: sec-agree
User-Agent: SM-T515-XXS4ATB4 Samsung IMS 6.0 DBT
Security-Verify: ipsec-3gpp;prot=esp;mod=trans;spi-c=112;spi-s=113;port-c=5106;port-s=6106;alg=hmac-sha-1-96;ealg=null
P-Preferred-Identity: <sip:001011234567893@ims.mnc001.mcc001.3gppnetwork.org>
Content-Length: 0

