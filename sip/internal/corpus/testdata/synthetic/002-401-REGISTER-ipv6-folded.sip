SIP/2.0 401 Unauthorized
Via: SIP/2.0/UDP [2001:db8:10::5]:5060;branch=z9hG4bK2a1b3c4d5e;rport=5060;received=2001:db8:10::5
From: <sip:001010000000001@ims.mnc001.mcc001.3gppnetwork.org>;tag=a1b2c3
To: <sip:001010000000001@ims.mnc001.mcc001.3gppnetwork.org>;tag=srv1
Call-ID: 4f6e2a10-0001@2001:db8:10::5
CSeq: 1 REGISTER
WWW-Authenticate: Digest realm="ims.mnc001.mcc001.3gppnetwork.org",
   nonce="3q2+7wAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
   algorithm=AKAv1-MD5,
   qop="auth"
Security-Server: ipsec-3gpp;prot=esp;mod=trans;spi-c=4098;spi-s=4099;port-c=5102;port-s=5103;alg=hmac-sha-1-96;ealg=null;q=0.1
Content-Length: 0

