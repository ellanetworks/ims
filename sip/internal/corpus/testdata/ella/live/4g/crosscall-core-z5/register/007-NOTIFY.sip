NOTIFY sip:1909008f-ca77-42f3-af80-b070b083f49f@10.46.0.8:40158 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKH2TJ5GZLNKSTX4WQMZJNM4VEZM
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKEVOTLQARZF6WS5INYZFJLSK2D2
Record-Route: <sip:aal3mjkgrd7rpdn7kqfwsmrmn6@10.80.0.5:5063;lr;r2=on;ue>
Record-Route: <sip:aal3mjkgrd7rpdn7kqfwsmrmn6@10.80.0.5:5060;lr;r2=on>
Max-Forwards: 69
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=KZRELU54OFQYOWKMWYZKN6N6SZ
To: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=2196987
Call-ID: 2196984_1761248032@10.46.0.8
CSeq: 1373224151 NOTIFY
Content-Length: 1866
Contact: <sip:10.80.0.5:5080>
Event: reg
Subscription-State: active;expires=600000
Content-Type: application/reginfo+xml

<?xml version="1.0" encoding="UTF-8"?>
<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="0" state="full"><registration aor="sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org" id="re7903526b6e30f72" state="active"><contact id="c2b1ca9abf4ef9bcc" state="active" event="created" expires="3600" callid="2196665_1761188768@10.46.0.8" cseq="2196666"><uri>sip:1909008f-ca77-42f3-af80-b070b083f49f@10.46.0.8:40158</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000002-0&gt;&#34;</unknown-param><unknown-param name="+g.3gpp.ps-data-off">&#34;inactive&#34;</unknown-param><unknown-param name="audio"></unknown-param><unknown-param name="+g.3gpp.nw-init-ussi"></unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="video"></unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param></contact></registration><registration aor="tel:15550000005" id="ra61e87ba10a68c98" state="active"><contact id="c938121d782a36a4f" state="active" event="created" expires="3600" callid="2196665_1761188768@10.46.0.8" cseq="2196666"><uri>sip:1909008f-ca77-42f3-af80-b070b083f49f@10.46.0.8:40158</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000002-0&gt;&#34;</unknown-param><unknown-param name="+g.3gpp.ps-data-off">&#34;inactive&#34;</unknown-param><unknown-param name="audio"></unknown-param><unknown-param name="+g.3gpp.nw-init-ussi"></unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="video"></unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param></contact></registration></reginfo>