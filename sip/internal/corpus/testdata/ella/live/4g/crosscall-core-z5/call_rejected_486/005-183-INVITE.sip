SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKHUURV72IQ5F3XAOCXVRHDRXONF,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKPFVT5PIF2KXJIDENQDGAJTW5DC,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bK5Q7XJN36BNDUCWW6BY6K3VWLFN,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKSNJGOZHPCA4K3JDX66LGA5MP6R,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK7ZHRWL22P6PRJZP2ZICP26VLVU,SIP/2.0/TCP 10.46.0.13:40279;branch=z9hG4bK398496443
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=263747
To: <tel:15550000004;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=264142
CSeq: 263739 INVITE
Call-ID: 263739_1760018656@10.46.0.13
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=QEKL32EPKUOGABT6;ue>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=QEKL32EPKUOGABT6>,<sip:mt@10.80.0.5:5080;lr;did=T7CQDBYIE5L6JSN5>,<sip:mo@10.80.0.5:5080;lr;did=NZYATJZMBQ5NAF66>,<sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5060;lr;r2=on;did=QSWJH7JYD4HQGDRM>,<sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5063;transport=tcp;lr;r2=on;did=QSWJH7JYD4HQGDRM;ue>
Content-Type: application/sdp
Content-Length: 479
Server: Core-Z5 N1982U.6.02.09.FR00
RSeq: 1
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015EE0000
Contact: <sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: 100rel,precondition
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS

v=0
o=- 8 1000 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50020 RTP/AVP 104 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
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
