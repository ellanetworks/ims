SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.0.0.2:46783;branch=z9hG4bKWWB2JEAIVIOONJKWBW4B7F4VCX;rport=35619;received=10.0.0.2
Record-Route: <sip:mzi3nxmn2cruqeuuhfm6h74r7b@10.0.0.1:5063;transport=tcp;lr;r2=on;did=XISKBLPJNSXSUBHY;ue>
Record-Route: <sip:mzi3nxmn2cruqeuuhfm6h74r7b@10.0.0.1:5060;lr;r2=on;did=XISKBLPJNSXSUBHY>
Record-Route: <sip:mt@10.0.0.1:5080;lr;did=MUBYW3Z3FNLTPYPU>
Record-Route: <sip:mo@10.0.0.1:5080;lr;did=32X5K3MYKIO55BCA>
Record-Route: <sip:u6ko3qca5otjtdr7t5r6mykajq@10.0.0.1:5060;lr;r2=on;did=KFLV5UAV3AZWW6BJ>
Record-Route: <sip:u6ko3qca5otjtdr7t5r6mykajq@10.0.0.1:5063;transport=tcp;lr;r2=on;did=KFLV5UAV3AZWW6BJ;ue>
From: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>;tag=FDVPAJ22ZGPUS67COMQ6HW7A5T
To: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=RVR5ABTUD7VHHB527SCXES5CWA
Call-ID: 665e1dd8-bf75-11f1-86b4-4d6ade81ba5e@10.0.0.2
CSeq: 1 INVITE
Content-Length: 455
Contact: <sip:66200811-bf75-11f1-86b4-4d6ade81ba5e@10.0.0.3:45849>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE
Supported: 100rel, timer, precondition
Content-Type: application/sdp
Require: precondition, 100rel
RSeq: 406976770
P-Asserted-Identity: <tel:+15550002>
P-Asserted-Identity: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>

v=0
o=- 117379211919370 0 IN IP4 10.0.0.3
s=-
c=IN IP4 10.0.0.3
t=0 0
m=audio 40002 RTP/AVP 116 111
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=220
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos mandatory remote sendrecv
a=conf:qos remote sendrecv
a=sendrecv
a=ptime:20
a=maxptime:240
