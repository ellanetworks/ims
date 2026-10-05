NOTIFY sip:7512e589-6633-4e25-a782-5db15e482347@10.46.0.3:40208 SIP/2.0
Via: SIP/2.0/TCP 10.80.0.5:5063;branch=z9hG4bK7DEALQ4IRFHW247LAQKFNQMBKF
Via: SIP/2.0/UDP 10.80.0.5:5080;branch=z9hG4bK5TEO7IH65K7XNFI4GXLC3PLV4A
Record-Route: <sip:qxiclaidlyttgghrzsp2cmlmtq@10.80.0.5:5063;lr;r2=on;ue>
Record-Route: <sip:qxiclaidlyttgghrzsp2cmlmtq@10.80.0.5:5060;lr;r2=on>
Max-Forwards: 69
From: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=ZYJA6LKPUE7SAV7RISWDW47IQH
To: <sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org>;tag=10552123
Call-ID: 10552121_3070524560@10.46.0.3
CSeq: 1440953596 NOTIFY
Content-Length: 1871
Contact: <sip:10.80.0.5:5080>
Event: reg
Subscription-State: active;expires=600000
Content-Type: application/reginfo+xml

<?xml version="1.0" encoding="UTF-8"?>
<reginfo xmlns="urn:ietf:params:xml:ns:reginfo" version="0" state="full"><registration aor="sip:15550000008@ims.mnc001.mcc999.3gppnetwork.org" id="r22e4307ec8725e30" state="active"><contact id="c421206ac530a7a72" state="active" event="created" expires="3600" callid="10551871_3068466336@10.46.0.3" cseq="10551872"><uri>sip:7512e589-6633-4e25-a782-5db15e482347@10.46.0.3:40208</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000003-0&gt;&#34;</unknown-param><unknown-param name="+g.3gpp.ps-data-off">&#34;inactive&#34;</unknown-param><unknown-param name="audio"></unknown-param><unknown-param name="+g.3gpp.nw-init-ussi"></unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="video"></unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param></contact></registration><registration aor="tel:+15550000008" id="r7076471236b28715" state="active"><contact id="ca11befedf302aa9d" state="active" event="created" expires="3600" callid="10551871_3068466336@10.46.0.3" cseq="10551872"><uri>sip:7512e589-6633-4e25-a782-5db15e482347@10.46.0.3:40208</uri><unknown-param name="+g.3gpp.accesstype">&#34;cellular2&#34;</unknown-param><unknown-param name="+sip.instance">&#34;&lt;urn:gsma:imei:00000000-000003-0&gt;&#34;</unknown-param><unknown-param name="+g.3gpp.ps-data-off">&#34;inactive&#34;</unknown-param><unknown-param name="audio"></unknown-param><unknown-param name="+g.3gpp.nw-init-ussi"></unknown-param><unknown-param name="+g.3gpp.smsip"></unknown-param><unknown-param name="video"></unknown-param><unknown-param name="+g.3gpp.icsi-ref">&#34;urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel&#34;</unknown-param></contact></registration></reginfo>