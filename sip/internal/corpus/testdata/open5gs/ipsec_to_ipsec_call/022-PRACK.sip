PRACK sip:0398765432100@192.168.101.2:6300;alias=192.168.101.2~6301~2 SIP/2.0
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK4a83.118c8bc64aaace269eb2ef9059d603b3.0
Via: SIP/2.0/UDP 10.4.128.21:6060;branch=z9hG4bK4a83.19ad26d3aca024b9bed33790685ca329.0
Via: SIP/2.0/UDP 10.4.128.21:5100;branch=z9hG4bK4a83.aee42600580a2caed1a7de78c4939f44.0
Via: SIP/2.0/UDP 192.168.101.3:6400;received=192.168.101.3;branch=z9hG4bK-524287-1---44c7f219aac590b6;rport=6401;transport=UDP
Max-Forwards: 67
Route: <sip:mt@10.4.128.21;r2=on;lr=on;ftag=7b3fae13;rm=7;did=078.754>
Route: <sip:mt@10.4.128.21:5100;r2=on;lr=on;ftag=7b3fae13;rm=7;did=078.754>
Proxy-Require: sec-agree
Require: sec-agree
To: <sip:0398765432100;phone-context=0498765432100@0498765432100;user=phone>;tag=4cbb9622
From: <tel:0498765432100>;tag=7b3fae13
Call-ID: TF0lR-e_t0o7Jp-w5kq58A..@192.168.101.3
CSeq: 2 PRACK
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, INFO, REFER, NOTIFY, MESSAGE, PRACK
Supported: sec-agree
User-Agent: SM-T515-TB4 Samsung IMS 6.0
Security-Verify: ipsec-3gpp;prot=esp;mod=trans;spi-c=102;spi-s=103;port-c=5101;port-s=6101;alg=hmac-sha-1-96;ealg=null
RAck: 1 1 INVITE
P-Preferred-Identity: <tel:0498765432100>
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=001010001000019b
Content-Length: 0
Supported: sec-agree
Security-Server: ipsec-3gpp;prot=esp;mod=trans;spi-c=100;spi-s=101;port-c=5100;port-s=6100;alg=hmac-sha-1-96;ealg=null

