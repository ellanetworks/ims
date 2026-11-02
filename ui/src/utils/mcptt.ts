import type { Group, MCPTTUser } from "@/queries/mcptt";

// RFC 3261 section 19.1.4
const URI = /^([a-z][a-z0-9+.-]*):(?:([^@;?]*)@)?([^;?]*)(.*)$/i;

export const identityKey = (uri: string): string => {
  const m = URI.exec(uri.trim());
  if (!m) return uri.trim();

  const scheme = m[1].toLowerCase() === "sips" ? "sip" : m[1].toLowerCase();
  const user = m[2] === undefined ? "" : `${m[2]}@`;

  return `${scheme}:${user}${m[3].toLowerCase()}`;
};

export const sameIdentity = (a: string, b: string): boolean =>
  identityKey(a) === identityKey(b);

// RFC 3261 section 25.1
const NAME = /^[A-Za-z0-9\-_.!~*'()&=+$,;?/]+$/;

// TS 23.280 clause 8.1.2
// TS 23.280 clause 8.1.3.1
export const expandID = (input: string, domain: string): string | undefined => {
  const s = input.trim();
  if (s === "") return undefined;
  if (/^sips?:/i.test(s)) return s;
  if (s.includes("@")) return `sip:${s}`;
  return NAME.test(s) ? `sip:${s}@${domain}` : undefined;
};

export const isMCPTTIdentity = (uri: string): boolean =>
  /^sips?:mcptt-[^@]+@/i.test(uri);

// TS 23.003 clause 13.4
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

export const groupsOf = (groups: Group[], id: string): Group[] =>
  groups.filter((g) => g.members.some((m) => sameIdentity(m.mcptt_id, id)));

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

export const formatDuration = (ms: number): string => {
  if (ms === 0) return "0 s";
  if (ms % 1000 !== 0) return `${ms} ms`;

  const s = ms / 1000;
  if (s % 60 !== 0) return `${s} s`;

  const min = s / 60;
  if (min % 60 !== 0 || min < 60) return `${min} min`;

  return `${min / 60} h`;
};
