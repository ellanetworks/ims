UPDATE sip:15550000002@10.46.0.3:49165 SIP/2.0
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=103310767
To: <tel:15550000002;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=b3bbc289
Call-ID: 103310760_1760534736@10.46.0.2
CSeq: 103310762 UPDATE
Content-Length: 454
Via: SIP/2.0/TCP 10.46.0.2:43300;branch=z9hG4bK99169867
Max-Forwards: 70
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=0010100000100066C000
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=191483176;spi-s=53304908;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5063;transport=tcp;lr;r2=on;did=KRJRUFGOJHVZWXI2;ue>,<sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5060;lr;r2=on;did=KRJRUFGOJHVZWXI2>,<sip:mo@10.80.0.5:5080;lr;did=YHNIGHXT5KSXB5PT>,<sip:mt@10.80.0.5:5080;lr;did=I74XPGSQO32BRJPH>,<sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5060;lr;r2=on;ob;did=P3M3RQZAT22TNGN3>,<sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5063;transport=tcp;lr;r2=on;ob;did=P3M3RQZAT22TNGN3;ue>
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Require: sec-agree,precondition
Proxy-Require: sec-agree
Contact: <sip:b394eba0-c3ed-4e54-adf4-83d317ca2979@10.46.0.2:43300>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi

v=0
o=- 13 1001 IN IP4 10.46.0.2
s=QC VOIP
c=IN IP4 10.46.0.2
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50024 RTP/AVP 104 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
