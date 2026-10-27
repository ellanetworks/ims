import type { Group, MCPTTUser } from "@/queries/mcptt";

// A URI as scheme:user@host[;params]: the parts the server tells identities apart by (RFC 3261 §19.1.4).
const URI = /^([a-z][a-z0-9+.-]*):(?:([^@;?]*)@)?([^;?]*)(.*)$/i;

// identityKey is the identity of a URI as the server compares them: the scheme and host case-insensitive, sips a
// sip, the user part as is.
export const identityKey = (uri: string): string => {
  const m = URI.exec(uri.trim());
  if (!m) return uri.trim();

  const scheme = m[1].toLowerCase() === "sips" ? "sip" : m[1].toLowerCase();
  const user = m[2] === undefined ? "" : `${m[2]}@`;

  return `${scheme}:${user}${m[3].toLowerCase()}`;
};

export const sameIdentity = (a: string, b: string): boolean =>
  identityKey(a) === identityKey(b);

// A name that expands to a URI: the characters of the user part of a SIP URI that need no escaping (RFC 3261 §25.1).
const NAME = /^[A-Za-z0-9\-_.!~*'()&=+$,;?/]+$/;

// expandID is the MCPTT ID or group ID an operator typed (TS 23.280 §8.1.2, §8.1.3.1): a URI as is, user@host a SIP
// URI, a bare name a SIP URI of the organisation's domain; undefined if none of them.
export const expandID = (input: string, domain: string): string | undefined => {
  const s = input.trim();
  if (s === "") return undefined;
  if (/^sips?:/i.test(s)) return s;
  if (s.includes("@")) return `sip:${s}`;
  return NAME.test(s) ? `sip:${s}@${domain}` : undefined;
};

// expandIMPU is the IMS public user identity an operator typed (TS 23.003 §13.4): a URI as is, user@host a SIP URI of
// it, user alone a SIP URI of the home domain, an E.164 number, with or without its +, the tel URI of it: the IMPU
// Ella Core's HSS gives a subscriber of that MSISDN, which the server compares exactly.
export const expandIMPU = (
  input: string,
  homeDomain: string,
): string | undefined => {
  const s = input.trim();
  if (s === "") return undefined;
  if (/^(sips?|tel):/i.test(s)) return s;
  if (s.includes("@")) return `sip:${s}`;
  if (/^\+?\d{1,15}$/.test(s))
    return `sip:mcptt-${s.replace(/^\+/, "")}@${homeDomain}`;
  return /^[A-Za-z0-9\-_.!~*'()]+$/.test(s)
    ? `sip:${s}@${homeDomain}`
    : undefined;
};

// shortID is how an identity shows: the user part of a SIP URI of the domain, else the URI without its scheme.
export const shortID = (uri: string, domain: string): string => {
  const m = URI.exec(uri);
  if (!m) return uri;

  if (
    m[2] !== undefined &&
    m[3].toLowerCase() === domain.toLowerCase() &&
    m[4] === ""
  ) {
    return m[2];
  }

  return uri.replace(/^sips?:/i, "");
};

export const userLabel = (u: MCPTTUser, domain: string): string =>
  u.alias || shortID(u.mcptt_id, domain);

export const groupLabel = (g: Group, domain: string): string =>
  g.display_name || shortID(g.group_id, domain);

// groupsOf is the groups the user of MCPTT ID id is a member of, in order.
export const groupsOf = (groups: Group[], id: string): Group[] =>
  groups.filter((g) => g.members.some((m) => sameIdentity(m.mcptt_id, id)));

// formatElapsed is the time from an instant to now, as 0:42, 12:05 or 1:02:03.
export const formatElapsed = (
  iso: string,
  now: number = Date.now(),
): string => {
  const total = Math.max(0, Math.floor((now - new Date(iso).getTime()) / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = String(total % 60).padStart(2, "0");

  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
};

// formatDuration is a duration in milliseconds as people read it: 1 h 30 min, 10 min, 30 s, 500 ms.
export const formatDuration = (ms: number): string => {
  if (ms === 0) return "0 s";
  if (ms % 1000 !== 0) return `${ms} ms`;

  const s = ms / 1000;
  if (s % 60 !== 0) return `${s} s`;

  const min = s / 60;
  if (min % 60 !== 0 || min < 60) return `${min} min`;

  return `${min / 60} h`;
};
