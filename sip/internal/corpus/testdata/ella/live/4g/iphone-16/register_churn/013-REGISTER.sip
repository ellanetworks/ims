REGISTER sip:ims.mnc001.mcc001.3gppnetwork.org SIP/2.0
To: <sip:001010000000003@ims.mnc001.mcc001.3gppnetwork.org>
From: <sip:001010000000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=Mcov4FzzIt
Expires: 600000
Require: sec-agree
Proxy-Require: sec-agree
Security-Client: ipsec-3gpp;alg=hmac-md5-96;ealg=aes-cbc;mod=trans;port-c=55863;port-s=65006;prot=esp;spi-c=89557690;spi-s=194252518,ipsec-3gpp;alg=hmac-md5-96;ealg=null;mod=trans;port-c=55863;port-s=65006;prot=esp;spi-c=89557690;spi-s=194252518,ipsec-3gpp;alg=hmac-sha-1-96;ealg=aes-cbc;mod=trans;port-c=55863;port-s=65006;prot=esp;spi-c=89557690;spi-s=194252518,ipsec-3gpp;alg=hmac-sha-1-96;ealg=null;mod=trans;port-c=55863;port-s=65006;prot=esp;spi-c=89557690;spi-s=194252518
Call-ID: TANy8spWzCOaV9CNalFUtERF
Session-ID: e840d735aaf97232fa722a18a5552f4c
Contact: <sip:10.46.0.18:65006>;+g.3gpp.accesstype="cellular2";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.smsip;+g.3gpp.srvcc-alerting;+sip.instance="<urn:gsma:imei:00000000-000005-0>";audio
CSeq: 5 REGISTER
Authorization: Digest qop=auth,nc=00000001,nonce="xO9lYYjpKqsTdTUdEw6PcQ0E06HUGIAAuameNIQpze4=",uri="sip:ims.mnc001.mcc001.3gppnetwork.org",realm="ims.mnc001.mcc001.3gppnetwork.org",cnonce="bszy7lE3",algorithm=AKAv1-MD5,username="001010000000003@ims.mnc001.mcc001.3gppnetwork.org",response="de62071492d82fe3bfedcfbf8cbda5b1"
Via: SIP/2.0/UDP 10.46.0.18:65006;branch=z9hG4bKZ7yb4wOVJ9l8piN;rport
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 70
Supported: 100rel,path,replaces
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Security-Verify: ipsec-3gpp;alg=hmac-sha-1-96;ealg=null;mod=trans;port-c=5065;port-s=5063;prot=esp;q=0.1;spi-c=155682339;spi-s=251587190
Content-Length: 0

