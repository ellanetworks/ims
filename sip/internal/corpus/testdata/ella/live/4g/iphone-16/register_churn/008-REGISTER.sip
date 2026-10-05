REGISTER sip:ims.mnc001.mcc001.3gppnetwork.org SIP/2.0
To: <sip:001010000000003@ims.mnc001.mcc001.3gppnetwork.org>
From: <sip:001010000000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=e7b0WZmIJm
Expires: 0
Require: sec-agree
Proxy-Require: sec-agree
Security-Client: ipsec-3gpp;alg=hmac-md5-96;ealg=aes-cbc;mod=trans;port-c=61267;port-s=52940;prot=esp;spi-c=150340838;spi-s=127799800,ipsec-3gpp;alg=hmac-md5-96;ealg=null;mod=trans;port-c=61267;port-s=52940;prot=esp;spi-c=150340838;spi-s=127799800,ipsec-3gpp;alg=hmac-sha-1-96;ealg=aes-cbc;mod=trans;port-c=61267;port-s=52940;prot=esp;spi-c=150340838;spi-s=127799800,ipsec-3gpp;alg=hmac-sha-1-96;ealg=null;mod=trans;port-c=61267;port-s=52940;prot=esp;spi-c=150340838;spi-s=127799800
Call-ID: TANy8spWzCOaV9CNalFUtERF
Session-ID: e840d735aaf97232fa722a18a5552f4c
Contact: <sip:10.46.0.18:52940>;+g.3gpp.accesstype="cellular2";+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+sip.instance="<urn:gsma:imei:00000000-000005-0>"
Authorization: Digest qop=auth,nc=00000002,nonce="eb03BTRdk96BTw4fpXkyWIiIh/TDaoAA6gNp3828Jls=",uri="sip:ims.mnc001.mcc001.3gppnetwork.org",realm="ims.mnc001.mcc001.3gppnetwork.org",cnonce="M099yqn6",algorithm=AKAv1-MD5,username="001010000000003@ims.mnc001.mcc001.3gppnetwork.org",response="2e1414b412a576ca126e7f99ab0c5991"
CSeq: 3 REGISTER
Via: SIP/2.0/UDP 10.46.0.18:52940;branch=z9hG4bKXu5LiK8n7NyGA1L;rport
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
Max-Forwards: 70
Supported: 100rel,path,replaces
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Security-Verify: ipsec-3gpp;alg=hmac-sha-1-96;ealg=null;mod=trans;port-c=5064;port-s=5063;prot=esp;q=0.1;spi-c=27476008;spi-s=116450528
Content-Length: 0

