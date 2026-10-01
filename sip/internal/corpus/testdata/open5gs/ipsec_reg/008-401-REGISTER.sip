SIP/2.0 401 Unauthorized - Challenging the UE
Via: SIP/2.0/UDP 192.168.101.5:5060;received=192.168.101.5;branch=z9hG4bK-524287-1---b18e79648f4c5de6;rport=5060;keep;transport=UDP
To: <sip:001011234567893@ims.mnc001.mcc001.3gppnetwork.org>;tag=a35a5806d6040414c4d26ea88c1e71a0-ec26680d
From: <sip:001011234567893@ims.mnc001.mcc001.3gppnetwork.org>;tag=c599e20e
Call-ID: 6wr5YA4uEzYn8TycDM-zOw..@192.168.101.5
CSeq: 1 REGISTER
WWW-Authenticate: Digest realm="ims.mnc001.mcc001.3gppnetwork.org", nonce="oJRMdf88TwhTovkQqh8QT8bi9GyPQoAAfzu5uEt8P/Y=", algorithm=AKAv1-MD5, ck="d53c02758b376066fad0af7daa6df765", ik="e1763cf28cc2e584588800193137ec92", qop="auth,auth-int"
Path: <sip:term@pcscf.ims.mnc001.mcc001.3gppnetwork.org;lr>
Server: Kamailio S-CSCF
Content-Length: 0
Supported: sec-agree
Security-Server: ipsec-3gpp;prot=esp;mod=trans;spi-c=100;spi-s=101;port-c=5100;port-s=6100;alg=hmac-md5-96;ealg=null

