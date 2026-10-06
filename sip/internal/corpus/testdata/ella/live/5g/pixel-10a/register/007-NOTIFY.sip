NOTIFY sip:1ca9266b-5791-f0ca-9a23-92da316d71a9@10.46.0.3:49165 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKWV5P7NNWY7TEOAFUJMMSSQS3SY
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKA63HJDUAFL2YFPHATEAAVY442B
Record-Route: <sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5063;transport=tcp;lr;r2=on;ue>
Record-Route: <sip:tkgtix7rbcrrsrqosvtxrd7mao@10.80.0.5:5060;lr;r2=on>
Max-Forwards: 69
From: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=WKUSKDY6YBCKJL76PDUCU7ENYS
To: <sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org>;tag=991a32fe
Call-ID: 7aea4c93342fa70@10.46.0.3
CSeq: 92062681 NOTIFY
Content-Length: 1791
Contact: <sip:10.80.0.5:5080>
Event: reg
Subscription-State: active;expires=600000
Content-Type: application/reginfo+xml

<?xml version="1.0" encoding="UTF-8"?>
<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="0" state="full"><registration aor="sip:15550000002@ims.mnc001.mcc001.3gppnetwork.org" id="r88b3a8b22a382499" state="active"><contact id="cbb22d8b16cd6a4d0" state="active" event="created" expires="3600" callid="bed67b5899ddd8f7@10.46.0.3" cseq="2"><uri>sip:1ca9266b-5791-f0ca-9a23-92da316d71a9@10.46.0.3:49165</uri><unknown-param name="audio"></unknown-param><unknown-param name="+g.3gpp.nw-init-ussi"></unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param><unknown-param name="video"></unknown-param><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="reg-id">1</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000006-0&gt;&#34;</unknown-param></contact></registration><registration aor="tel:+15550000002" id="rd1fd47c2132d2ef8" state="active"><contact id="c82e1cbff152d6bbc" state="active" event="created" expires="3600" callid="bed67b5899ddd8f7@10.46.0.3" cseq="2"><uri>sip:1ca9266b-5791-f0ca-9a23-92da316d71a9@10.46.0.3:49165</uri><unknown-param name="audio"></unknown-param><unknown-param name="+g.3gpp.nw-init-ussi"></unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param><unknown-param name="video"></unknown-param><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="reg-id">1</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000006-0&gt;&#34;</unknown-param></contact></registration></reginfo>