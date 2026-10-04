SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKFR7RT5XSHS6GFLSXFINNNUJLB2,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKBR5X2K7QHZW6G3ZIATTUYS7D46,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKUQSETJKE7RJM3RR7SUTY7SSGUN,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKNILFBP4UE7DG53CENYPPQTNLRK,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKFNTMA2G4X5OJ7JYMEATKQXJNE4,SIP/2.0/TCP 10.46.0.15:43435;branch=z9hG4bK815309176
From: <sip:15550000004@ims.mnc001.mcc001.3gppnetwork.org>;tag=2285999
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=xlYCbExxPl
CSeq: 2285993 INVITE
Call-ID: 2285993_3069112144@10.46.0.15
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=OZ5L5WPXEWEZT5DE;lr;r2=on;transport=tcp;ue>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=OZ5L5WPXEWEZT5DE;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=WDR4TWYSLKD4FGVV;lr>, <sip:mo@10.80.0.5:5080;did=GBN5MJRLAQ7OKFUP;lr>, <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5060;did=D3QLDSGEZTLKUJJE;lr;r2=on>, <sip:de3wrydi5e3hcnhjmlpzs4qzn5@10.80.0.5:5063;did=D3QLDSGEZTLKUJJE;lr;r2=on;transport=tcp;ue>
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uac
Require: 100rel,precondition,timer
RSeq: 1
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Content-Type: application/sdp
Content-Length: 516

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791147846 0 IN IP4 10.46.0.17
s=-
c=IN IP4 10.46.0.17
b=AS:41
b=RS:0
b=RR:0
t=0 0
m=audio 49120 RTP/AVP 104 96
b=AS:41
b=RS:0
b=RR:0
a=rtpmap:104 AMR-WB/16000
a=fmtp:104 max-red=0; mode-change-capability=2
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=des:qos mandatory local sendrecv
a=curr:qos local none
a=des:qos mandatory remote sendrecv
a=curr:qos remote none
a=conf:qos remote sendrecv
