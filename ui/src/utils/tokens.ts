export interface Tokens {
  primary: string;
  success: string;
  error: string;
  warning: string;
  info: string;
  backgroundDefault: string;
  backgroundPaper: string;
  textPrimary: string;
  textSecondary: string;
}

export const light: Tokens = {
  primary: "#26374A",
  success: "#1B6C1C",
  error: "#C62828",
  warning: "#ED6C02",
  info: "#037EAA",
  backgroundDefault: "#FFFFFF",
  backgroundPaper: "#FFFFFF",
  textPrimary: "rgba(0, 0, 0, 0.87)",
  textSecondary: "rgba(0, 0, 0, 0.6)",
};

export const dark: Tokens = {
  primary: "#5B9DFF",
  success: "#4ABF4B",
  error: "#EC9393",
  warning: "#F09142",
  info: "#19AFE6",
  backgroundDefault: "#14161B",
  backgroundPaper: "#1C2027",
  textPrimary: "#E3E5EA",
  textSecondary: "rgba(227, 229, 234, 0.66)",
};
