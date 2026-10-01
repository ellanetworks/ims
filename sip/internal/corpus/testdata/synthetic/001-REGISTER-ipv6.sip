REGISTER sip:ims.mnc001.mcc001.3gppnetwork.org SIP/2.0
Via: SIP/2.0/UDP [2001:db8:10::5]:5060;branch=z9hG4bK2a1b3c4d5e;rport
Max-Forwards: 70
From: <sip:001010000000001@ims.mnc001.mcc001.3gppnetwork.org>;tag=a1b2c3
To: <sip:001010000000001@ims.mnc001.mcc001.3gppnetwork.org>
Call-ID: 4f6e2a10-0001@2001:db8:10::5
CSeq: 1 REGISTER
Contact: <sip:001010000000001@[2001:db8:10::5]:5060>;+sip.instance="<urn:gsma:imei:00000000-000000-0>";+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.smsip;audio;expires=600000
Authorization: Digest username="001010000000001@ims.mnc001.mcc001.3gppnetwork.org",realm="ims.mnc001.mcc001.3gppnetwork.org",nonce="",uri="sip:ims.mnc001.mcc001.3gppnetwork.org",response=""
Security-Client: ipsec-3gpp;prot=esp;mod=trans;spi-c=4096;spi-s=4097;port-c=50001;port-s=50000;alg=hmac-sha-1-96;ealg=null,ipsec-3gpp;prot=esp;mod=trans;spi-c=4096;spi-s=4097;port-c=50001;port-s=50000;alg=hmac-sha-1-96;ealg=aes-cbc
Require: sec-agree
Proxy-Require: sec-agree
Supported: path,gruu,sec-agree
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,OPTIONS,NOTIFY,REFER,INFO
P-Access-Network-Info: 3GPP-NR-FDD;utran-cell-id-3gpp=00101000001000000001
Expires: 600000
User-Agent: Synthetic-UE/1.0
Content-Length: 0

