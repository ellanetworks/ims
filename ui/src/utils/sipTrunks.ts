const LABEL = /^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/;

export const isHostname = (host: string): boolean => {
  if (host === "" || host.length > 253) return false;
  const labels = host.split(".");
  return labels.every((l) => LABEL.test(l)) && /^[A-Za-z]/.test(labels.at(-1)!);
};

const isIP = (host: string): boolean =>
  /^\d{1,3}(\.\d{1,3}){3}$/.test(host) || host.includes(":");

const addUnique = (list: string[], value: string) => {
  if (!list.some((v) => v.toLowerCase() === value.toLowerCase())) {
    list.push(value);
  }
};

export interface TrunkDefaults {
  domain: string;
  inboundCidrs: string[];
  identities: string[];
}

export const trunkDefaults = (
  hosts: string[],
  domain: string,
  trusts: boolean,
): TrunkDefaults => {
  const names: string[] = [];
  const inboundCidrs: string[] = [];
  for (const host of hosts) {
    if (isHostname(host)) addUnique(names, host);
    else if (isIP(host))
      addUnique(inboundCidrs, `${host}/${host.includes(":") ? 128 : 32}`);
  }

  const effectiveDomain = domain !== "" ? domain : (names[0] ?? "");
  const identities: string[] = [];
  if (trusts) {
    if (isHostname(effectiveDomain)) identities.push(effectiveDomain);
    for (const name of names) addUnique(identities, name);
  }

  return { domain: names[0] ?? "", inboundCidrs, identities };
};
