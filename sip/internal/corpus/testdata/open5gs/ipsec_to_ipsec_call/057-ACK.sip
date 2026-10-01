ACK sip:0398765432100@192.168.101.2:6300;alias=192.168.101.2~6301~2 SIP/2.0
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK7a83.b6259f08f0b4e25f5f1809883dd5cb62.0
Via: SIP/2.0/UDP 10.4.128.21:5100;branch=z9hG4bK7a83.c12ab59f8d180cac695a4bf8585ca956.0
Via: SIP/2.0/UDP 192.168.101.3:6400;received=192.168.101.3;branch=z9hG4bK-524287-1---42d9494fdfbcab96;rport=6401;transport=UDP
Max-Forwards: 68
Route: <sip:mt@10.4.128.21:6060;lr=on;ftag=7b3fae13;did=078.5c52>
Route: <sip:mt@10.4.128.21;r2=on;lr=on;ftag=7b3fae13;rm=7;did=078.754>
Route: <sip:mt@10.4.128.21:5100;r2=on;lr=on;ftag=7b3fae13;rm=7;did=078.754>
Proxy-Require: sec-agree
Require: sec-agree
To: <sip:0398765432100;phone-context=0498765432100@0498765432100;user=phone>;tag=4cbb9622
From: <tel:0498765432100>;tag=7b3fae13
Call-ID: TF0lR-e_t0o7Jp-w5kq58A..@192.168.101.3
CSeq: 1 ACK
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Supported: sec-agree
User-Agent: SM-T515-TB4 Samsung IMS 6.0
Security-Verify: ipsec-3gpp;prot=esp;mod=trans;spi-c=102;spi-s=103;port-c=5101;port-s=6101;alg=hmac-sha-1-96;ealg=null
P-Preferred-Identity: <tel:0498765432100>
Content-Length: 0
Supported: sec-agree
Security-Server: ipsec-3gpp;prot=esp;mod=trans;spi-c=100;spi-s=101;port-c=5100;port-s=6100;alg=hmac-sha-1-96;ealg=null

