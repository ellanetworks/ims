// TS 23.003 clause 13.2
export const derivedDomain = (mcc: string, mnc: string) =>
  `ims.mnc${mnc.padStart(3, "0")}.mcc${mcc}.3gppnetwork.org`;

export const homeDomain = (operator: {
  mcc: string;
  mnc: string;
  domain: string;
}) => operator.domain || derivedDomain(operator.mcc, operator.mnc);

// RFC 1035 section 2.3.1
// RFC 1123 section 2.1
export const domainError = (domain: string) => {
  if (domain === "") return undefined;
  if (domain.length > 253) return "At most 253 characters";
  const labels = domain.split(".");
  if (labels.length < 2) return "At least two labels, like ims.example.com";
  if (labels.some((l) => !/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(l)))
    return "Lowercase letters, digits and hyphens, in labels of 1 to 63 characters";
  if (/^\d+$/.test(labels[labels.length - 1]))
    return "A domain name, not an IP address";
  return undefined;
};
