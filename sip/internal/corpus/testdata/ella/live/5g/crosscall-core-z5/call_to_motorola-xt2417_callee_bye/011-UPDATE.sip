UPDATE sip:a87a2e9a-4c32-439f-afc0-825ea2f8b236@10.46.0.9:43885 SIP/2.0
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=111129102
To: <tel:15550000008;phone-context=ims.mnc001.mcc001.3gppnetwork.org>;tag=111129380
CSeq: 111129097 UPDATE
Call-ID: 111129095_1760121744@10.46.0.8
Content-Length: 471
Via: SIP/2.0/TCP 10.46.0.8:42225;branch=z9hG4bK958408246
Max-Forwards: 70
P-Access-Network-Info: 3GPP-NR-TDD;utran-cell-id-3gpp=0010100000100066C000
Security-Verify: ipsec-3gpp;q=0.1;prot=esp;mod=trans;spi-c=154382768;spi-s=92741926;port-c=5064;port-s=5063;alg=hmac-sha-1-96;ealg=null
Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5063;transport=tcp;lr;r2=on;did=LL6SDLDVCSAJDYPS;ue>,<sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5060;lr;r2=on;did=LL6SDLDVCSAJDYPS>,<sip:mo@10.80.0.5:5080;lr;did=BLJOGDFGWN6KFWQH>,<sip:mt@10.80.0.5:5080;lr;did=XI53LLJHEXYGXJ7S>,<sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5060;lr;r2=on;did=UADNKONHYWGY3ZQG>,<sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5063;transport=tcp;lr;r2=on;did=UADNKONHYWGY3ZQG;ue>
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Type: application/sdp
Require: sec-agree,precondition
Proxy-Require: sec-agree
Contact: <sip:2da86196-c8f7-4939-af8b-fbdac4e983f2@10.46.0.8:42225>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi

v=0
o=- 19 1001 IN IP4 10.46.0.8
s=QC VOIP
c=IN IP4 10.46.0.8
b=AS:37
b=RS:600
b=RR:2000
t=0 0
m=audio 50048 RTP/AVP 104 96
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
