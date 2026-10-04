SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKGNVVT4H5DD7OUQUBK6CLKVF3VD,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKEYKDDKIGR6VT2HNAZETGQ6T665,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKBXGIFHIVP6T5AARFJLPJ5Z3YUB,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKLYILWNU5642IQ2E7BC6HFFMMM6,SIP/2.0/TCP 10.46.0.15:43435;branch=z9hG4bK458296834
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=956055
To: <tel:15550000001;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=956312
CSeq: 956050 UPDATE
Call-ID: 956048_3070404240@10.46.0.15
Content-Length: 476
Record-Route: <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;transport=tcp;lr;r2=on;did=2K6RPNQ77TYFOPHJ;ue>,<sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;lr;r2=on;did=2K6RPNQ77TYFOPHJ>,<sip:mt@10.80.0.5:5080;lr;did=HTNEZOIG3W7NPTNH>,<sip:mo@10.80.0.5:5080;lr;did=GWICAY2QGKOEVK47>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=TLHZAYZAOS2BJJTQ>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=TLHZAYZAOS2BJJTQ;ue>
Content-Type: application/sdp
Server: PRD-IR92/12 term-Motorola/XT2417-1-V1UFN35H.193-20 device-type/smart-phone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015EE0000
Contact: <sip:59ca0608-182c-4e87-aed8-aa1e2a898c87@10.46.0.14:40265>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition
Supported: timer,100rel,from-change,tdialog
Privacy: id
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Session-Expires: 1800;refresher=uas

v=0
o=- 5 1001 IN IP4 10.46.0.14
s=QC VOIP
c=IN IP4 10.46.0.14
b=AS:37
b=RS:600
b=RR:2000
t=0 0
m=audio 50042 RTP/AVP 104 96
b=AS:37
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-set=3,4,5,6;mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
