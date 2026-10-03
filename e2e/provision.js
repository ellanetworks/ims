const K = "465b5ce8b199b49faa5f0a2ee238a6bc";
const OPC = "cd63cb71954a9f4e48a5994e37a02baf";

const subscribers = [
  { imsi: "001010000000001", msisdn: "15550000001" },
  { imsi: "001010000000002", msisdn: "15550000002" },
];

const bitrate = (v) => ({ value: NumberInt(v), unit: NumberInt(1) });

const arp = (level, capability, vulnerability) => ({
  priority_level: NumberInt(level),
  pre_emption_capability: NumberInt(capability),
  pre_emption_vulnerability: NumberInt(vulnerability),
});

const pccRule = (qci, level) => ({
  qos: {
    index: NumberInt(qci),
    arp: arp(level, 2, 2),
    mbr: { downlink: bitrate(128), uplink: bitrate(128) },
    gbr: { downlink: bitrate(128), uplink: bitrate(128) },
  },
  flow: [],
});

db = db.getSiblingDB("open5gs");

for (const s of subscribers) {
  db.subscribers.replaceOne(
    { imsi: s.imsi },
    {
      schema_version: NumberInt(1),
      imsi: s.imsi,
      msisdn: [s.msisdn],
      imeisv: [],
      mme_host: [],
      mm_realm: [],
      purge_flag: [],
      slice: [{
        sst: NumberInt(1),
        default_indicator: true,
        session: [{
          name: "ims",
          type: NumberInt(1),
          qos: { index: NumberInt(5), arp: arp(1, 1, 1) },
          ambr: { downlink: bitrate(3850), uplink: bitrate(1530) },
          pcc_rule: [pccRule(1, 2), pccRule(2, 4)],
        }],
      }],
      security: { k: K, op: null, opc: OPC, amf: "8000", sqn: NumberLong(0) },
      ambr: { downlink: bitrate(1000000), uplink: bitrate(1000000) },
      access_restriction_data: NumberInt(32),
      network_access_mode: NumberInt(0),
      subscriber_status: NumberInt(0),
      operator_determined_barring: NumberInt(0),
      subscribed_rau_tau_timer: NumberInt(12),
      __v: NumberInt(0),
    },
    { upsert: true },
  );
}

printjson(db.subscribers.find({}, { imsi: 1, msisdn: 1, _id: 0 }).toArray());
