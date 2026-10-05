REGISTER sip:ims.mnc001.mcc001.3gppnetwork.org SIP/2.0
To: <sip:001010000000006@ims.mnc001.mcc001.3gppnetwork.org>
From: <sip:001010000000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=LpxKkN42b0
Expires: 600000
Require: sec-agree
Proxy-Require: sec-agree
Security-Client: ipsec-3gpp;alg=hmac-md5-96;ealg=aes-cbc;mod=trans;port-c=59928;port-s=56059;prot=esp;spi-c=94219960;spi-s=210451499,ipsec-3gpp;alg=hmac-md5-96;ealg=null;mod=trans;port-c=59928;port-s=56059;prot=esp;spi-c=94219960;spi-s=210451499,ipsec-3gpp;alg=hmac-sha-1-96;ealg=aes-cbc;mod=trans;port-c=59928;port-s=56059;prot=esp;spi-c=94219960;spi-s=210451499,ipsec-3gpp;alg=hmac-sha-1-96;ealg=null;mod=trans;port-c=59928;port-s=56059;prot=esp;spi-c=94219960;spi-s=210451499
Call-ID: UzMyDuSCJFHdwcWqjUtQI3Mr
Session-ID: e6b8ed0d5decb5c6d0d60e3c393f48bd
Contact: <sip:10.46.0.16:56059>;+g.3gpp.accesstype="cellular2";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.smsip;+g.3gpp.srvcc-alerting;+sip.instance="<urn:gsma:imei:00000000-000004-0>";audio
CSeq: 2 REGISTER
Authorization: Digest qop=auth,nc=00000001,nonce="u9RzEbqPR056xWPdNz3VWpdWKVnbAoAADVpbFM2RT4w=",uri="sip:ims.mnc001.mcc001.3gppnetwork.org",realm="ims.mnc001.mcc001.3gppnetwork.org",cnonce="BPMCiInc",algorithm=AKAv1-MD5,username="001010000000006@ims.mnc001.mcc001.3gppnetwork.org",response="e0b85679916f8c40775eb32806ed7d33"
Via: SIP/2.0/TCP 10.46.0.16:56059;branch=z9hG4bK0lITMsuMHYuAT1t;rport
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 70
Supported: 100rel,path,replaces
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Security-Verify: ipsec-3gpp;alg=hmac-sha-1-96;ealg=null;mod=trans;port-c=5064;port-s=5063;prot=esp;q=0.1;spi-c=243438823;spi-s=105173783
Content-Length: 0

