export const hostOf = (addrPort: string): string => {
  const bracketed = /^\[(.*)\]:\d+$/.exec(addrPort);
  if (bracketed) return bracketed[1];

  const i = addrPort.lastIndexOf(":");
  return i < 0 ? addrPort : addrPort.slice(0, i);
};

export const portOf = (addrPort: string): number =>
  Number(addrPort.slice(addrPort.lastIndexOf(":") + 1));

export const formatEndpoint = (address: string, port: number): string =>
  address.includes(":") ? `[${address}]:${port}` : `${address}:${port}`;

export const pcscfAddresses = (
  listeners: { role: string; address: string }[],
): { ip: string; port: number }[] => {
  const seen = new Set<string>();
  return listeners
    .filter((l) => l.role === "pcscf")
    .map((l) => ({ ip: hostOf(l.address), port: portOf(l.address) }))
    .filter(({ ip }) => !seen.has(ip) && seen.add(ip));
};
