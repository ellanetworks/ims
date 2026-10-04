SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKDHHWMBWXDH6FQDKKWQUQULSN43,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKVRYX7TBHM7E5ABAQA6KKJRB52K,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKL7WX3JPYX7M7LOKSH3N5ZCCVEH,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKR5JJAJPQCKJ3DYJFORYMTKWPYH,SIP/2.0/TCP 10.46.0.14:40265;branch=z9hG4bK2328952648
From: <sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org>;tag=877628
To: <sip:15550000004;phone-context=ims.mnc001.mcc001.3gppnetwork.org@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=877916
CSeq: 877623 UPDATE
Call-ID: 877621_4111242528@10.46.0.14
Content-Length: 457
Record-Route: <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;transport=tcp;lr;r2=on;did=SDDNIER5RJ7WQSTR;ue>,<sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;lr;r2=on;did=SDDNIER5RJ7WQSTR>,<sip:mt@10.80.0.5:5080;lr;did=4JPO4OY5BD5KRJD2>,<sip:mo@10.80.0.5:5080;lr;did=T45IOCLIMXJ7ZJJC>,<sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5060;lr;r2=on;did=MEC3I4VACMXMXODM>,<sip:glnc67uf4gsp6g6ksmjaiyh4mr@10.80.0.5:5063;transport=tcp;lr;r2=on;did=MEC3I4VACMXMXODM;ue>
Content-Type: application/sdp
Server: Core-Z5 N1982U.6.02.09.FR00
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015EE0000
Contact: <sip:ca20d2b6-2524-424e-ac97-40fff9783fc4@10.46.0.15:43435>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi
Require: precondition
Supported: timer,100rel,from-change,tdialog
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Session-Expires: 1800;refresher=uas

v=0
o=- 14 1001 IN IP4 10.46.0.15
s=QC VOIP
c=IN IP4 10.46.0.15
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50046 RTP/AVP 99 96
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:99 AMR-WB/16000/1
a=fmtp:99 mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=curr:qos local sendrecv
a=curr:qos remote sendrecv
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
