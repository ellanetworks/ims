UPDATE sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435 SIP/2.0
From: <sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org>;tag=783166
To: <sip:15550000004;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=783471
CSeq: 783162 UPDATE
Call-ID: 783160_4111218192@10.46.0.14
Content-Length: 452
Via: SIP/2.0/TCP 10.46.0.14:40265;branch=z9hG4bK99095873
Max-Forwards: 70
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015EE0000
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=165898036;spi-s=190332794;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
Route: <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;transport=tcp;lr;r2=on;did=WZXMYJTSBXRSVS7Z;ue>,<sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;lr;r2=on;did=WZXMYJTSBXRSVS7Z>,<sip:mo@10.80.0.5:5080;lr;did=BL5YC3Z366BQXNXG>,<sip:mt@10.80.0.5:5080;lr;did=5VVWDAZQ3SKV2LSA>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=2SKLSZIK7BAOI2ED>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=2SKLSZIK7BAOI2ED;ue>
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: PRD-IR92/12 term-Motorola/XT2417-1-V1UFN35H.193-20 device-type/smart-phone
Content-Type: application/sdp
Require: sec-agree,precondition
Proxy-Require: sec-agree
Contact: <sip:59ca0608-182c-4e87-aed8-aa1e2a898c87@10.46.0.14:40265>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi

v=0
o=- 2 1001 IN IP4 10.46.0.14
s=QC VOIP
c=IN IP4 10.46.0.14
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50038 RTP/AVP 99 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:99 AMR-WB/16000/1
a=fmtp:99 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
