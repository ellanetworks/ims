INVITE sip:a87a2e9a-4c32-439f-afc0-825ea2f8b236@10.46.0.9:43885 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKWGNWO2A3JBOL7723QX7E73WQAY
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKKWGGTMRUWOSMZ6NAVSXT3W2T76
Via: SIP/2.0/UDP 10.80.0.5:5070;branch=z9hG4bKGBEYDZDKHGN2FTZFCLUMK5IHFR
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKPXLA6AV5NAWS2JESLAHQNM2MII
Via: SIP/2.0/UDP 10.80.0.5:5060;branch=z9hG4bK7UYKQ5Z7ZVWWMW7E4WYZKU6T4W
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=111129102
To: <tel:15550000008;phone-context=ims.mnc001.mcc001.3gppnetwork.org>
CSeq: 111129095 INVITE
Call-ID: 111129095_1760121744@10.46.0.8
Via: SIP/2.0/TCP 10.46.0.8:42225;branch=z9hG4bK2684443074
Record-Route: <sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5063;transport=tcp;lr;r2=on;did=UADNKONHYWGY3ZQG;ue>
Record-Route: <sip:nsbgwhufa64gwwi7ngbk7jry6d@10.80.0.5:5060;lr;r2=on;did=UADNKONHYWGY3ZQG>
Record-Route: <sip:mt@10.80.0.5:5080;lr;did=XI53LLJHEXYGXJ7S>
Record-Route: <sip:mo@10.80.0.5:5080;lr;did=BLJOGDFGWN6KFWQH>
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5060;lr;r2=on;did=LL6SDLDVCSAJDYPS>
Record-Route: <sip:l2aykhrmqrls3wwcj6g7e6j3yy@10.80.0.5:5063;transport=tcp;lr;r2=on;did=LL6SDLDVCSAJDYPS;ue>
Max-Forwards: 65
Contact: <sip:2da86196-c8f7-4939-af8b-fbdac4e983f2@10.46.0.8:42225>;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio;video;+g.3gpp.nw-init-ussi;+g.3gpp.mid-call;+g.3gpp.srvcc-alerting;+g.3gpp.ps2cs-srvcc-orig-pre-alerting
Allow: INVITE,ACK,CANCEL,BYE,UPDATE,PRACK,MESSAGE,REFER,NOTIFY,INFO,OPTIONS
Content-Type: application/sdp
Accept: application/sdp,application/3gpp-ims+xml
Accept-Contact: *;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel";audio
Supported: timer,199,100rel,replaces,precondition,from-change,histinfo,tdialog
User-Agent: Core-Z5 N1982U.6.02.09.FR00
Content-Length: 762
Session-Expires: 1800;refresher=uac
P-Called-Party-ID: <tel:+15550000008>
P-Early-Media: supported
P-Asserted-Identity: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>
P-Asserted-Identity: <tel:+15550000005>

v=0
o=- 19 1000 IN IP4 10.46.0.8
s=QC VOIP
c=IN IP4 10.46.0.8
b=AS:41
b=RS:600
b=RR:2000
t=0 0
m=audio 50048 RTP/AVP 104 110 102 108 96 97
b=AS:41
b=RS:600
b=RR:2000
a=rtpmap:104 AMR-WB/16000/1
a=fmtp:104 mode-change-capability=2;max-red=0
a=rtpmap:110 AMR-WB/16000/1
a=fmtp:110 octet-align=1;mode-change-capability=2;max-red=0
a=rtpmap:102 AMR/8000/1
a=fmtp:102 mode-change-capability=2;max-red=0
a=rtpmap:108 AMR/8000/1
a=fmtp:108 octet-align=1;mode-change-capability=2;max-red=0
a=rtpmap:96 telephone-event/16000
a=fmtp:96 0-15
a=rtpmap:97 telephone-event/8000
a=fmtp:97 0-15
a=curr:qos local none
a=curr:qos remote none
a=des:qos mandatory local sendrecv
a=des:qos optional remote sendrecv
a=sendrecv
a=maxptime:240
a=ptime:20
