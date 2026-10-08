// homeDomain is the IMS home domain of an operator, with a 2-digit MNC padded to 3 (TS 23.003 §13.2).
export const homeDomain = (mcc: string, mnc: string) =>
  `ims.mnc${mnc.padStart(3, "0")}.mcc${mcc}.3gppnetwork.org`;
