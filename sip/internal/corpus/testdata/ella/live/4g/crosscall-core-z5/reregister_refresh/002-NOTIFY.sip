NOTIFY sip:6360b6ba-8a1f-493d-a05a-f4121465d17e@10.46.0.13:40279 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKZH4QYWN4VIJJE2A5N4BAM57NIA
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bKDWVK6UPLPLZ7KJJGWDEIQNQNUP
Record-Route: <sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5063;lr;r2=on;ue>
Record-Route: <sip:mwfvpmdjn5gmmwmnbhkzroke5x@10.80.0.5:5060;lr;r2=on>
Max-Forwards: 69
From: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=UUEGYYDQEUQWYA7Z4B6Y5UUVIE
To: <sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org>;tag=25577
Call-ID: 25574_1761429312@10.46.0.13
CSeq: 1116654716 NOTIFY
Content-Length: 1867
Contact: <sip:10.80.0.5:5080>
Event: reg
Subscription-State: active;expires=597001
Content-Type: application/reginfo+xml

<?xml version="1.0" encoding="UTF-8"?>
<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="1" state="full"><registration aor="sip:15550000005@ims.mnc001.mcc001.3gppnetwork.org" id="re7903526b6e30f72" state="active"><contact id="c337c37dd815bd3a1" state="active" event="refreshed" expires="3600" callid="25241_1760322288@10.46.0.13" cseq="25243"><uri>sip:6360b6ba-8a1f-493d-a05a-f4121465d17e@10.46.0.13:40279</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000002-0&gt;&#34;</unknown-param><unknown-param name="+g.3gpp.ps-data-off">&#34;inactive&#34;</unknown-param><unknown-param name="audio"></unknown-param><unknown-param name="+g.3gpp.nw-init-ussi"></unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="video"></unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param></contact></registration><registration aor="tel:+15550000005" id="redf68b6fd5f9ea70" state="active"><contact id="c2daec4a52756c584" state="active" event="refreshed" expires="3600" callid="25241_1760322288@10.46.0.13" cseq="25243"><uri>sip:6360b6ba-8a1f-493d-a05a-f4121465d17e@10.46.0.13:40279</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000002-0&gt;&#34;</unknown-param><unknown-param name="+g.3gpp.ps-data-off">&#34;inactive&#34;</unknown-param><unknown-param name="audio"></unknown-param><unknown-param name="+g.3gpp.nw-init-ussi"></unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="video"></unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param></contact></registration></reginfo>