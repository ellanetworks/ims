// TS 23.003 clause 13.2
export const homeDomain = (mcc: string, mnc: string) =>
  `ims.mnc${mnc.padStart(3, "0")}.mcc${mcc}.3gppnetwork.org`;
