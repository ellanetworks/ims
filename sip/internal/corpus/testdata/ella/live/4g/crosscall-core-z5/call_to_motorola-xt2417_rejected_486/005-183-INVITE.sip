SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK6X3O6O33U6WV5CIOAYRMDKZBDO,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKBMDX7XJRKUFE5V4ASZ36YIS5LL,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK2YQA6IQEQMI43WWFJGFM2KRH4L,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKPEBVLRZT35KP4Q7D3ERFKUZ6E4,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKZO72VOH6SVFVM4WHZPMV7C5ZKR,SIP/2.0/TCP 10.46.0.15:43435;branch=z9hG4bK3730375334
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=1010352
To: <tel:15550000001;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=1010648
CSeq: 1010344 INVITE
Call-ID: 1010344_3070881744@10.46.0.15
Record-Route: <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;transport=tcp;lr;r2=on;did=3DPP7RBC7WAICPDY;ue>,<sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;lr;r2=on;did=3DPP7RBC7WAICPDY>,<sip:mt@10.80.0.5:5080;lr;did=Y3JVOJ2BRXD732RM>,<sip:mo@10.80.0.5:5080;lr;did=WJDJJFTDKG4B3CTP>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=WOASPHOUYCYYOTQ6>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=WOASPHOUYCYYOTQ6;ue>
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
o=- 6 1000 IN IP4 10.46.0.14
s=QC VOIP
c=IN IP4 10.46.0.14
b=AS:37
b=RS:600
b=RR:2000
t=0 0
m=audio 50032 RTP/AVP 104 96
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
