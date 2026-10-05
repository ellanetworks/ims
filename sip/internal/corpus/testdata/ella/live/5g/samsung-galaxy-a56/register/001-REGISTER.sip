REGISTER sip:ims.mnc001.mcc999.3gppnetwork.org SIP/2.0
Via: SIP/2.0/TCP 10.46.0.2:5060;branch=z9hG4bK-524287-1---ba7d7ef7a718e060;rport;keep;transport=TCP
Max-Forwards: 70
Proxy-Require: sec-agree
Require: sec-agree
Contact: <sip:999010000000007@10.46.0.2:5060>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";q=1.0;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.smsip
To: <sip:999010000000007@ims.mnc001.mcc999.3gppnetwork.org>
From: <sip:999010000000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=4fdf2453
Call-ID: Z2DA--Y-Vm1l6HrBSerjCw..@10.46.0.2
CSeq: 1 REGISTER
Expires: 600000
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Supported: path, gruu, sec-agree
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
Authorization: Digest username="999010000000007@ims.mnc001.mcc999.3gppnetwork.org",realm="ims.mnc001.mcc999.3gppnetwork.org",uri="sip:ims.mnc001.mcc999.3gppnetwork.org",nonce="",response="",algorithm=AKAv1-MD5
Security-Client: ipsec-3gpp;prot=esp;mod=trans;spi-c=40021;spi-s=40022;port-c=6401;port-s=6400;alg=hmac-md5-96;ealg=des-ede3-cbc, ipsec-3gpp;prot=esp;mod=trans;spi-c=40021;spi-s=40022;port-c=6401;port-s=6400;alg=hmac-md5-96;ealg=aes-cbc, ipsec-3gpp;prot=esp;mod=trans;spi-c=40021;spi-s=40022;port-c=6401;port-s=6400;alg=hmac-md5-96;ealg=null, ipsec-3gpp;prot=esp;mod=trans;spi-c=40021;spi-s=40022;port-c=6401;port-s=6400;alg=hmac-sha-1-96;ealg=des-ede3-cbc, ipsec-3gpp;prot=esp;mod=trans;spi-c=40021;spi-s=40022;port-c=6401;port-s=6400;alg=hmac-sha-1-96;ealg=aes-cbc, ipsec-3gpp;prot=esp;mod=trans;spi-c=40021;spi-s=40022;port-c=6401;port-s=6400;alg=hmac-sha-1-96;ealg=null
Content-Length: 0

