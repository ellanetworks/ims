REGISTER sip:ims.mnc001.mcc999.3gppnetwork.org SIP/2.0
Via: SIP/2.0/TCP 10.46.0.2:6400;branch=z9hG4bK-524287-1---8235176e6e8d278b;rport;keep;transport=TCP
Max-Forwards: 70
Proxy-Require: sec-agree
Require: sec-agree
Contact: <sip:999010000000007@10.46.0.2:6400>;+sip.instance="<urn:gsma:imei:00000000-000007-0>";q=1.0;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.smsip
To: <sip:999010000000007@ims.mnc001.mcc999.3gppnetwork.org>
From: <sip:999010000000007@ims.mnc001.mcc999.3gppnetwork.org>;tag=4fdf2453
Call-ID: Z2DA--Y-Vm1l6HrBSerjCw..@10.46.0.2
CSeq: 2 REGISTER
Expires: 600000
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Supported: path, gruu, sec-agree
User-Agent: SM-A566W-A566WVLS6BZB7 Samsung IMS 6.0
Authorization: Digest username="999010000000007@ims.mnc001.mcc999.3gppnetwork.org",realm="ims.mnc001.mcc999.3gppnetwork.org",uri="sip:ims.mnc001.mcc999.3gppnetwork.org",nonce="jo2BY8bGSrWCrj42HC3Fu1W6h/vnY4AAMD0WPL+Egb8=",response="e0cb9b2855b2d7a32c9217426734701d",algorithm=AKAv1-MD5,nc=00000001,qop=auth,cnonce="e70c271f8c39c00835a6df80d3c67f05"
Security-Client: ipsec-3gpp;prot=esp;mod=trans;spi-c=40021;spi-s=40022;port-c=6401;port-s=6400;alg=hmac-md5-96;ealg=des-ede3-cbc, ipsec-3gpp;prot=esp;mod=trans;spi-c=40021;spi-s=40022;port-c=6401;port-s=6400;alg=hmac-md5-96;ealg=aes-cbc, ipsec-3gpp;prot=esp;mod=trans;spi-c=40021;spi-s=40022;port-c=6401;port-s=6400;alg=hmac-md5-96;ealg=null, ipsec-3gpp;prot=esp;mod=trans;spi-c=40021;spi-s=40022;port-c=6401;port-s=6400;alg=hmac-sha-1-96;ealg=des-ede3-cbc, ipsec-3gpp;prot=esp;mod=trans;spi-c=40021;spi-s=40022;port-c=6401;port-s=6400;alg=hmac-sha-1-96;ealg=aes-cbc, ipsec-3gpp;prot=esp;mod=trans;spi-c=40021;spi-s=40022;port-c=6401;port-s=6400;alg=hmac-sha-1-96;ealg=null
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=30560604;spi-s=41880514;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=9990100000100066c000
Content-Length: 0

