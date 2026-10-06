PRACK sip:15550000002@10.46.0.3:49165 SIP/2.0
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=103428407
To: <tel:15550000002;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=fb185c80
Call-ID: 103428400_1759955408@10.46.0.2
CSeq: 103428401 PRACK
Content-Length: 454
Via: SIP/2.0/TCP 10.46.0.2:43300;branch=z9hG4bK3515531254
Max-Forwards: 70
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=0010100000100066C000
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=191483176;spi-s=53304908;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
RAck: 1 103428400 INVITE
Route: <sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5063;transport=tcp;lr;r2=on;did=27E46R75JD77JBKM;ue>,<sip:tmmgbfsyubr4jlhnlazmeepxie@10.80.0.5:5060;lr;r2=on;did=27E46R75JD77JBKM>,<sip:mo@10.80.0.5:5080;lr;did=CX45PVPUGXCTRVNS>,<sip:mt@10.80.0.5:5080;lr;did=RKCP4XAE5ZE445PP>,<sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5060;lr;r2=on;ob;did=GVR3JWODM4TFC7BI>,<sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5063;transport=tcp;lr;r2=on;ob;did=GVR3JWODM4TFC7BI;ue>
Content-Type: application/sdp
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Proxy-Require: sec-agree
Require: sec-agree,precondition
Supported: timer

v=0
o=- 14 1001 IN IP4 10.46.0.2
s=QC VOIP
c=IN IP4 10.46.0.2
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50032 RTP/AVP 104 96
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
