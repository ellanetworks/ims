SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKBXHD7I34REZQCH7O3O6W2U5ZVN,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKNJQQUN37GZKTKGNDWSI6KKFRLX,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKH77JKOWXKFCCPQFXMS7RNXXUCM,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bKRVEK565BYAKCDKEWT6UOY26QZN,SIP/2.0/TCP 10.46.0.17:61189;branch=z9hG4bKjgFosKDPnH7iquH;received=10.46.0.17;rport=57130
To: <tel:+15550000003>;tag=6M2cC6shoR
From: <sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org>;tag=JfQdEX0VUc
Call-ID: gUiVFxQ4a0Ah6UrdbS8lTcmy
Session-ID: 5fbc98a363d0ccc4ad24641f83c911c7
CSeq: 3 UPDATE
Record-Route: <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5063;did=ZB25FCIOS5KMME6C;lr;r2=on;ue>, <sip:yxlpwprxmpbiklyoarggpjtb5s@10.80.0.5:5060;did=ZB25FCIOS5KMME6C;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=XK5SSXCAFFQYR3Z6;lr>, <sip:mo@10.80.0.5:5080;did=PWN32BO6FZUKSXZO;lr>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=QQGC5BFXP6ZX3JKD;lr;r2=on>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=QQGC5BFXP6ZX3JKD;lr;r2=on;transport=tcp;ue>
Require: precondition,timer
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uac
Contact: <sip:10.46.0.20:53718>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Content-Type: application/sdp
Content-Length: 529

v=0
o=sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org 1791149447 1 IN IP4 10.46.0.20
s=-
c=IN IP4 10.46.0.20
b=AS:30
b=RS:375
b=RR:1125
t=0 0
m=audio 49120 RTP/AVP 116 105
b=AS:30
b=RS:375
b=RR:1125
a=rtpmap:116 EVS/16000
a=fmtp:116 br=13.2; bw=nb-wb; max-red=220
a=rtpmap:105 telephone-event/16000
a=fmtp:105 0-15
a=ptime:20
a=maxptime:240
a=sendrecv
a=des:qos mandatory local sendrecv
a=curr:qos local sendrecv
a=des:qos mandatory remote sendrecv
a=curr:qos remote sendrecv
a=conf:qos remote sendrecv
