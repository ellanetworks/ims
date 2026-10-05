SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKV3MFHQDSPW3BYCNUNFDTX2OLP7,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK6D6TF6SI46QCTBIP3QJ7NGIZZY,SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKFRF55CEXZKJPYX7D3ZMSSOBZWD,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKBWLMSSAAKVZW2MYOR6EULLHRZH,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKZAXRABGZQO6GWCIUXDMEITED4V,SIP/2.0/TCP 10.46.0.24:49165;branch=z9hG4bKa84352eb77e1b54f6f13f56f
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=5dbd15fa
To: <tel:15550000006;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=4OowVpBWzJ
Call-ID: 2fe61b022ddfe082@10.46.0.24
CSeq: 1 INVITE
Record-Route: <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5063;did=BP4ZIIWE7C4BAQTB;lr;r2=on;transport=tcp;ue>, <sip:f3o7bgwjgdlapb2kgsbgpxeghm@10.80.0.5:5060;did=BP4ZIIWE7C4BAQTB;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=GPUBPJIVDZ5BJCZ4;lr>, <sip:mo@10.80.0.5:5080;did=NDT3AW34I34W4GUJ;lr>, <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5060;did=IDI2ZWJH2JYXERQO;lr;r2=on>, <sip:kieypu6ghclrut4m5ob2zhojji@10.80.0.5:5063;did=IDI2ZWJH2JYXERQO;lr;r2=on;transport=tcp;ue>
Session-ID: 7f855f9310a1594980f338b8521adda3
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uac
Require: 100rel,precondition,timer
RSeq: 1
Contact: <sip:10.46.0.25:63507>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";+g.3gpp.mid-call;+g.3gpp.ps2cs-srvcc-orig-pre-alerting;+g.3gpp.srvcc-alerting;audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Content-Type: application/sdp
Content-Length: 510

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791202664 0 IN IP4 10.46.0.25
s=-
c=IN IP4 10.46.0.25
b=AS:42
b=RS:0
b=RR:0
t=0 0
m=audio 49120 RTP/AVP 127 96
b=AS:42
b=RS:0
b=RR:0
a=rtpmap:127 EVS/16000
a=fmtp:127 br=5.9-24.4; bw=nb-wb; max-red=0
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
