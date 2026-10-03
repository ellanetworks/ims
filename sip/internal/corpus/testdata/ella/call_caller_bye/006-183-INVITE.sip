SIP/2.0 183 Session Progress
Via: SIP/2.0/TCP 10.0.0.2:38889;branch=z9hG4bKN2W5RLM7OASABZ6SP32GPA23SA;rport=44803;received=10.0.0.2
Record-Route: <sip:zfh2lgtqhm5cnong5mbtqesooq@10.0.0.1:5063;transport=tcp;lr;r2=on;did=OEMTOR2JHJYATYM3;ue>
Record-Route: <sip:zfh2lgtqhm5cnong5mbtqesooq@10.0.0.1:5060;lr;r2=on;did=OEMTOR2JHJYATYM3>
Record-Route: <sip:mt@10.0.0.1:5080;lr;did=HIBKNCKAGBRTF3WP>
Record-Route: <sip:mo@10.0.0.1:5080;lr;did=UDWTBO5ZV72KW7J2>
Record-Route: <sip:fch555hos5mgc3rtk3lovlciov@10.0.0.1:5060;lr;r2=on;did=3WVWD42GP5R4HKAL>
Record-Route: <sip:fch555hos5mgc3rtk3lovlciov@10.0.0.1:5063;transport=tcp;lr;r2=on;did=3WVWD42GP5R4HKAL;ue>
From: <sip:+15550001@ims.mnc001.mcc001.3gppnetwork.org>;tag=PW7SQXHCQTXO6C5IMA73U6MNJA
To: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>;tag=DLT2KVKAMKLPY4ILHK37GIPRQX
Call-ID: 657c1234-bf75-11f1-86b4-4d6ade81ba5e@10.0.0.2
CSeq: 1 INVITE
Content-Length: 307
Contact: <sip:656c296f-bf75-11f1-86b4-4d6ade81ba5e@10.0.0.3:46761>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Allow: INVITE, ACK, OPTIONS, CANCEL, BYE, UPDATE, PRACK, NOTIFY, MESSAGE
Supported: 100rel, timer
Content-Type: application/sdp
Require: 100rel
RSeq: 827209909
P-Asserted-Identity: <tel:+15550002>
P-Asserted-Identity: <sip:+15550002@ims.mnc001.mcc001.3gppnetwork.org;user=phone>

v=0
o=- 117379211919362 0 IN IP4 10.0.0.3
s=-
c=IN IP4 10.0.0.3
t=0 0
m=audio 40000 RTP/AVP 116 111
b=AS:41
b=RS:512
b=RR:1537
a=rtpmap:116 AMR-WB/16000/1
a=fmtp:116 mode-change-capability=2;max-red=220
a=rtpmap:111 telephone-event/16000
a=fmtp:111 0-15
a=sendrecv
a=ptime:20
a=maxptime:240
