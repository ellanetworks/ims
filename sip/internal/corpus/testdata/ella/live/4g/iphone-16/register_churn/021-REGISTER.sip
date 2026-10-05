REGISTER sip:ims.mnc001.mcc001.3gppnetwork.org SIP/2.0
To: <sip:001010000000003@ims.mnc001.mcc001.3gppnetwork.org>
From: <sip:001010000000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=BPV8YrEF5Y
Expires: 600000
Require: sec-agree
Proxy-Require: sec-agree
Security-Client: ipsec-3gpp;alg=hmac-md5-96;ealg=aes-cbc;mod=trans;port-c=51250;port-s=54286;prot=esp;spi-c=160174564;spi-s=81373319,ipsec-3gpp;alg=hmac-md5-96;ealg=null;mod=trans;port-c=51250;port-s=54286;prot=esp;spi-c=160174564;spi-s=81373319,ipsec-3gpp;alg=hmac-sha-1-96;ealg=aes-cbc;mod=trans;port-c=51250;port-s=54286;prot=esp;spi-c=160174564;spi-s=81373319,ipsec-3gpp;alg=hmac-sha-1-96;ealg=null;mod=trans;port-c=51250;port-s=54286;prot=esp;spi-c=160174564;spi-s=81373319
Call-ID: TANy8spWzCOaV9CNalFUtERF
Session-ID: e840d735aaf97232fa722a18a5552f4c
Contact: <sip:10.46.0.19:54286>;+g.3gpp.accesstype="cellular2";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.smsip;+g.3gpp.srvcc-alerting;+sip.instance="<urn:gsma:imei:00000000-000005-0>";audio
CSeq: 7 REGISTER
Authorization: Digest qop=auth,nc=00000001,nonce="qlU4NZ7AboHBRnmWVvJQY+LMDjmiSoAAH5Q6PRwKFdw=",uri="sip:ims.mnc001.mcc001.3gppnetwork.org",realm="ims.mnc001.mcc001.3gppnetwork.org",cnonce="n6m6QQ4X",algorithm=AKAv1-MD5,username="001010000000003@ims.mnc001.mcc001.3gppnetwork.org",response="721bd57689bd0f686a324993732e57ed"
Via: SIP/2.0/UDP 10.46.0.19:54286;branch=z9hG4bKGxOCtj6Q9HgkAFP;rport
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 70
Supported: 100rel,path,replaces
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Security-Verify: ipsec-3gpp;alg=hmac-sha-1-96;ealg=null;mod=trans;port-c=5064;port-s=5063;prot=esp;q=0.1;spi-c=227428419;spi-s=59663401
Content-Length: 0

