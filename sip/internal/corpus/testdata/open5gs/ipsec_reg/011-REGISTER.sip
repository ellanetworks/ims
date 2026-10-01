REGISTER sip:ims.mnc001.mcc001.3gppnetwork.org SIP/2.0
Via: SIP/2.0/UDP 10.4.128.21;branch=z9hG4bKad81.f1ad33bd56f4ade3cdd55b0a2ebd8951.1;i=1
Via: SIP/2.0/TCP 192.168.101.5:6300;received=192.168.101.5;branch=z9hG4bK-524287-1---e249c61f12365417;rport=6301;keep;transport=TCP
Max-Forwards: 69
Proxy-Require: sec-agree
Contact: <sip:192.168.101.5:6300;alias=192.168.101.5~6301~2>;+sip.instance="<urn:gsma:imei:35622410-483840-0>";q=1.0;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.smsip
To: <sip:001011234567893@ims.mnc001.mcc001.3gppnetwork.org>
From: <sip:001011234567893@ims.mnc001.mcc001.3gppnetwork.org>;tag=c599e20e
Call-ID: 6wr5YA4uEzYn8TycDM-zOw..@192.168.101.5
CSeq: 2 REGISTER
Expires: 3600
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
User-Agent: SM-T515-TB4 Samsung IMS 6.0
Authorization: Digest username="001011234567893@ims.mnc001.mcc001.3gppnetwork.org",realm="ims.mnc001.mcc001.3gppnetwork.org",uri="sip:ims.mnc001.mcc001.3gppnetwork.org",nonce="oJRMdf88TwhTovkQqh8QT8bi9GyPQoAAfzu5uEt8P/Y=",response="63175c06a7a88bd479f5c9f714487476",algorithm=AKAv1-MD5,nc=00000001,qop=auth-int,cnonce="a61fbe8516b77984b522256da5f7c32f"
Security-Client: ipsec-3gpp;prot=esp;mod=trans;spi-c=25656;spi-s=25657;port-c=6301;port-s=6300;alg=hmac-md5-96;ealg=null
Security-Verify: ipsec-3gpp;prot=esp;mod=trans;spi-c=100;spi-s=101;port-c=5100;port-s=6100;alg=hmac-md5-96;ealg=null
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001000019b
Content-Length: 0
Path: <sip:term@pcscf.ims.mnc001.mcc001.3gppnetwork.org;lr>
Supported: path
Require: path
P-Visited-Network-ID: ims.mnc001.mcc001.3gppnetwork.org

