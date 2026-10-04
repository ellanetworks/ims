UPDATE sip:59ca0608-182c-4e87-aed8-aa1e2a898c87@10.46.0.14:40265 SIP/2.0
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=1081653
To: <tel:15550000001;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=1082012
CSeq: 1081650 UPDATE
Call-ID: 1081648_3070637664@10.46.0.15
Content-Length: 473
Via: SIP/2.0/TCP 10.46.0.15:43435;branch=z9hG4bK1743954519
Max-Forwards: 70
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015EE0000
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=113224940;spi-s=105724704;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=ELPHFW53K3TWHBU5;ue>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=ELPHFW53K3TWHBU5>,<sip:mo@10.80.0.5:5080;lr;did=F5ZMBW5OX6DMYV43>,<sip:mt@10.80.0.5:5080;lr;did=NVJIYNISUBJNVM6T>,<sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;lr;r2=on;did=2UMOUCFMC5ZDMWNV>,<sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;transport=tcp;lr;r2=on;did=2UMOUCFMC5ZDMWNV;ue>
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Require: sec-agree,precondition
Proxy-Require: sec-agree
Contact: <sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi

v=0
o=- 17 1001 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:37
b=RS:600
b=RR:2000
t=0 0
m=audio 50040 RTP/AVP 104 96
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
