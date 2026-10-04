SIP/2.0 200 OK
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKJ7XO5UGYIAVDEGHN4WIX2FN2QV,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKX7S7AMY6PL6DPZDOGUEZKJBKP7,SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKVD7GQKJGGBFU4XXIAF5AU7X6MW,SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK7WCSGTT56ZNDFJU243HUVZENN6,SIP/2.0/UDP 10.46.0.19:54286;branch=z9hG4bKLijqWmzN2NoUX0E;received=10.46.0.19;rport=54286
To: <tel:+15550000006>;tag=hCj5citYZC
From: <sip:15550000003@ims.mnc001.mcc001.3gppnetwork.org>;tag=qFoDhZ8t3I
Call-ID: CbGVybVmwugZ5NcE1udfVl2Z
Session-ID: 214e9273547593d0a7821e7caa4e266d
CSeq: 3 UPDATE
Record-Route: <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5063;did=F5UCRKKYIXMYNXA3;lr;r2=on;transport=tcp;ue>, <sip:byfnpmnrebl3w4rtfu7vl6aath@10.80.0.5:5060;did=F5UCRKKYIXMYNXA3;lr;r2=on>, <sip:mt@10.80.0.5:5080;did=MYUSMYNP223LEG6B;lr>, <sip:mo@10.80.0.5:5080;did=V4AD5R6ITUR3MFX6;lr>, <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5060;did=SQDVSKQGRKXLGWGR;lr;r2=on>, <sip:x32syljhtyphttpzlk7ukklwna@10.80.0.5:5063;did=SQDVSKQGRKXLGWGR;lr;r2=on;ue>
Require: precondition,timer
Supported: 100rel,path,precondition,replaces,timer
Session-Expires: 1800;refresher=uas
Contact: <sip:10.46.0.17:61189>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: ACK,BYE,CANCEL,INFO,INVITE,MESSAGE,NOTIFY,OPTIONS,PRACK,REFER,UPDATE
User-Agent: iOS/27.0 iPhone
P-Access-Network-Info: 3GPP-E-UTRAN-FDD;utran-cell-id-3gpp=0010100015ee0000
Content-Type: application/sdp
Content-Length: 523

v=0
o=sip:15550000006@ims.mnc001.mcc001.3gppnetwork.org 1791148939 1 IN IP4 10.46.0.17
s=-
c=IN IP4 10.46.0.17
b=AS:42
b=RS:0
b=RR:0
t=0 0
m=audio 49120 RTP/AVP 116 105
b=AS:42
b=RS:0
b=RR:0
a=rtpmap:116 EVS/16000
a=fmtp:116 br=5.9-24.4; bw=nb-wb; max-red=220
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
