SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKFHOVJ5FFADSGGGOGEFIW44AKYZ,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKL2WZHVMMSC7C7ZC3NJRFYPZOBR,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK7GLSN4VDTLDQC7MOLPZBKV4ZZY,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKDWZ7JL3CVK3QZLGKZPO2W56XDI,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKAH2C4Y6OZST4MANHAQI6X5BWY6,SIP/2.0/TCP 10.46.0.14:40265;branch=z9hG4bK1442902918
From: <sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org>;tag=783166
To: <sip:15550000004;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=783471
CSeq: 783160 INVITE
Call-ID: 783160_4111218192@10.46.0.14
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=2SKLSZIK7BAOI2ED;ue>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=2SKLSZIK7BAOI2ED>,<sip:mt@10.80.0.5:5080;lr;did=5VVWDAZQ3SKV2LSA>,<sip:mo@10.80.0.5:5080;lr;did=BL5YC3Z366BQXNXG>,<sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;lr;r2=on;did=WZXMYJTSBXRSVS7Z>,<sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;transport=tcp;lr;r2=on;did=WZXMYJTSBXRSVS7Z;ue>
Content-Type: application/sdp
Content-Length: 477
Server: Core-Z5 N1982U.6.02.09.FR00
RSeq: 1
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015EE0000
Contact: <sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: 100rel,precondition
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS

v=0
o=- 12 1000 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50028 RTP/AVP 99 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:99 AMR-WB/16000/1
a=fmtp:99 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=conf:qos remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
