UPDATE sip:59ca0608-182c-4e87-aed8-aa1e2a898c87@10.46.0.14:40265 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKAQSYC7RIBOKN5LIGZENNLLPMSN
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKUGGNRJZ432DSGUIE3LU4JG6L3Y
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK723IMTQXVHFFEYPFOWS5QGKMAZ
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKIJK3UWXKQ7HRGPBBSQR63IMFX6
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=1081653
To: <tel:15550000001;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=1082012
CSeq: 1081650 UPDATE
Call-ID: 1081648_3070637664@10.46.0.15
Content-Length: 473
Via: SIP/2.0/TCP 10.46.0.15:43435;branch=z9hG4bK1743954519
Record-Route: <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;transport=tcp;lr;r2=on;did=2UMOUCFMC5ZDMWNV;ue>
Record-Route: <sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;lr;r2=on;did=2UMOUCFMC5ZDMWNV>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=NVJIYNISUBJNVM6T>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=F5ZMBW5OX6DMYV43>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=ELPHFW53K3TWHBU5>
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=ELPHFW53K3TWHBU5;ue>
Max-Forwards: 66
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Contact: <sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition

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
