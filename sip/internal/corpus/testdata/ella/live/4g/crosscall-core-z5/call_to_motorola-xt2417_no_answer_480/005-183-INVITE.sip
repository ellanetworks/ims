SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKYIUDVWRCVRZBY4BYTDJLR7KLEH,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKXO22Q7KZGHRTR6Y5EVK4VPH7SK,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKNXWCCSKD7NBSOJJWMVKTKAK4Q7,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKCZWRF4WCDTYPVSF773DB7L4AEL,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKEQC3WQPETH7U3KUP4ZZJ2COVT3,SIP/2.0/TCP 10.46.0.15:43435;branch=z9hG4bK4289813359
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=1081653
To: <tel:15550000001;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=1082012
CSeq: 1081648 INVITE
Call-ID: 1081648_3070637664@10.46.0.15
Record-Route: <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;transport=tcp;lr;r2=on;did=2UMOUCFMC5ZDMWNV;ue>,<sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;lr;r2=on;did=2UMOUCFMC5ZDMWNV>,<sip:mt@10.80.0.5:5080;lr;did=NVJIYNISUBJNVM6T>,<sip:mo@10.80.0.5:5080;lr;did=F5ZMBW5OX6DMYV43>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=ELPHFW53K3TWHBU5>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=ELPHFW53K3TWHBU5;ue>
Content-Type: application/sdp
Content-Length: 496
Server: PRD-IR92/12 term-Motorola/XT2417-1-V1UFN35H.193-20 device-type/smart-phone
RSeq: 1
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015EE0000
Contact: <sip:59ca0608-182c-4e87-aed8-aa1e2a898c87@10.46.0.14:40265>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: 100rel,precondition
Privacy: id
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS

v=0
o=- 7 1000 IN IP4 10.46.0.14
s=QC VOIP
c=IN IP4 10.46.0.14
b=AS:37
b=RS:600
b=RR:2000
t=0 0
m=audio 50060 RTP/AVP 104 96
b=AS:37
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-set=3,4,5,6;mode-change-capability=2;max-red=0
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
