REGISTER sip:ims.mnc001.mcc001.3gppnetwork.org SIP/2.0
Via: SIP/2.0/UDP 10.4.128.21;branch=z9hG4bKdd81.ba4880ca97acea5143ad30eaa3c9a19c.1
Via: SIP/2.0/UDP 192.168.101.5:5060;received=192.168.101.5;branch=z9hG4bK-524287-1---b18e79648f4c5de6;rport=5060;keep;transport=UDP
Max-Forwards: 69
Proxy-Require: sec-agree
Contact: <sip:192.168.101.5:5060;alias=192.168.101.5~5060~1>;+sip.instance="<urn:gsma:imei:35622410-483840-0>";q=1.0;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.smsip
To: <sip:001011234567893@ims.mnc001.mcc001.3gppnetwork.org>
From: <sip:001011234567893@ims.mnc001.mcc001.3gppnetwork.org>;tag=c599e20e
Call-ID: 6wr5YA4uEzYn8TycDM-zOw..@192.168.101.5
CSeq: 1 REGISTER
Expires: 3600
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
User-Agent: SM-T515-TB4 Samsung IMS 6.0
Authorization: Digest username="001011234567893@ims.mnc001.mcc001.3gppnetwork.org",realm="ims.mnc001.mcc001.3gppnetwork.org",uri="sip:ims.mnc001.mcc001.3gppnetwork.org",nonce="",response="",algorithm=AKAv1-MD5
Security-Client: ipsec-3gpp;prot=esp;mod=trans;spi-c=25656;spi-s=25657;port-c=6301;port-s=6300;alg=hmac-md5-96;ealg=null
Content-Length: 0
Path: <sip:term@pcscf.ims.mnc001.mcc001.3gppnetwork.org;lr>
Supported: path
Require: path
P-Visited-Network-ID: ims.mnc001.mcc001.3gppnetwork.org

