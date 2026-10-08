// hostOf is the IP address of an address and port, "192.0.2.1:5060" or "[2001:db8::1]:5060".
export const hostOf = (addrPort: string): string => {
  const bracketed = /^\[(.*)\]:\d+$/.exec(addrPort);
  if (bracketed) return bracketed[1];

  const i = addrPort.lastIndexOf(":");
  return i < 0 ? addrPort : addrPort.slice(0, i);
};

// portOf is the port of an address and port, "192.0.2.1:5060" or "[2001:db8::1]:5060".
export const portOf = (addrPort: string): number =>
  Number(addrPort.slice(addrPort.lastIndexOf(":") + 1));

export const formatEndpoint = (address: string, port: number): string =>
  address.includes(":") ? `[${address}]:${port}` : `${address}:${port}`;
