NOTIFY sip:1b4261b8-485f-4f9e-a1af-589ac5cae854@10.46.0.7:41498 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bKBP3EDQNLMAQBDFA2AEFJX24TPI
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK7T62FA2BK6HH6CF6BJT7KXSTBE
Record-Route: <sip:czkbfhadzsd73oei7gc663rtsz@10.80.0.5:5063;lr;r2=on;ue>
Record-Route: <sip:czkbfhadzsd73oei7gc663rtsz@10.80.0.5:5060;lr;r2=on>
Max-Forwards: 69
From: <sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org>;tag=WXC67CMWFYCEMLZWU5VRG32DSP
To: <sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org>;tag=2181183
Call-ID: 2181181_4111069120@10.46.0.7
CSeq: 457163164 NOTIFY
Content-Length: 1866
Contact: <sip:10.80.0.5:5080>
Event: reg
Subscription-State: active;expires=600000
Content-Type: application/reginfo+xml

<?xml version="1.0" encoding="UTF-8"?>
<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="0" state="full"><registration aor="sip:15550000001@ims.mnc001.mcc001.3gppnetwork.org" id="r57c0916cf3867058" state="active"><contact id="cb0dae370f17df2d7" state="active" event="created" expires="3600" callid="2180901_4111866560@10.46.0.7" cseq="2180902"><uri>sip:1b4261b8-485f-4f9e-a1af-589ac5cae854@10.46.0.7:41498</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000001-0&gt;&#34;</unknown-param><unknown-param name="+g.3gpp.ps-data-off">&#34;inactive&#34;</unknown-param><unknown-param name="audio"></unknown-param><unknown-param name="+g.3gpp.nw-init-ussi"></unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="video"></unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param></contact></registration><registration aor="tel:15550000001" id="rbbeaf10a59e25db1" state="active"><contact id="ca605a64cd04d698c" state="active" event="created" expires="3600" callid="2180901_4111866560@10.46.0.7" cseq="2180902"><uri>sip:1b4261b8-485f-4f9e-a1af-589ac5cae854@10.46.0.7:41498</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000001-0&gt;&#34;</unknown-param><unknown-param name="+g.3gpp.ps-data-off">&#34;inactive&#34;</unknown-param><unknown-param name="audio"></unknown-param><unknown-param name="+g.3gpp.nw-init-ussi"></unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="video"></unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param></contact></registration></reginfo>