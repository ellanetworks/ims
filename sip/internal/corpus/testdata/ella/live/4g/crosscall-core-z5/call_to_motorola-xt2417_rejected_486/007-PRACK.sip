PRACK sip:59ca0608-182c-4e87-aed8-aa1e2a898c87@10.46.0.14:40265 SIP/2.0
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=1010352
To: <tel:15550000001;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=1010648
CSeq: 1010345 PRACK
Call-ID: 1010344_3070881744@10.46.0.15
Content-Length: 473
Via: SIP/2.0/TCP 10.46.0.15:43435;branch=z9hG4bK1587578122
Max-Forwards: 70
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015EE0000
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=113224940;spi-s=105724704;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
RAck: 1 1010344 INVITE
Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=WOASPHOUYCYYOTQ6;ue>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=WOASPHOUYCYYOTQ6>,<sip:mo@10.80.0.5:5080;lr;did=WJDJJFTDKG4B3CTP>,<sip:mt@10.80.0.5:5080;lr;did=Y3JVOJ2BRXD732RM>,<sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;lr;r2=on;did=3DPP7RBC7WAICPDY>,<sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;transport=tcp;lr;r2=on;did=3DPP7RBC7WAICPDY;ue>
Content-Type: application/sdp
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Proxy-Require: sec-agree
Require: sec-agree,precondition
Supported: timer

v=0
o=- 16 1001 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:37
b=RS:600
b=RR:2000
t=0 0
m=audio 50020 RTP/AVP 104 96
b=AS:37
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-set=3,4,5,6;mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
