export interface Tokens {
  primary: string;
  success: string;
  error: string;
  warning: string;
  info: string;
  link: string;
  backgroundDefault: string;
  backgroundPaper: string;
  backgroundSubtle: string;
  textPrimary: string;
  textSecondary: string;
  borderControl: string;
}

export const light: Tokens = {
  primary: "#26374A",
  success: "#1B6C1C",
  error: "#C62828",
  warning: "#ED6C02",
  info: "#037EAA",
  link: "#2B3FD4",
  backgroundDefault: "#FFFFFF",
  backgroundPaper: "#FFFFFF",
  backgroundSubtle: "#E2E2E2",
  textPrimary: "rgba(0, 0, 0, 0.87)",
  textSecondary: "rgba(0, 0, 0, 0.6)",
  borderControl: "rgba(0, 0, 0, 0.42)",
};

export const dark: Tokens = {
  primary: "#5B9DFF",
  success: "#4ABF4B",
  error: "#EC9393",
  warning: "#F09142",
  info: "#19AFE6",
  link: "#97A3F7",
  backgroundDefault: "#14161B",
  backgroundPaper: "#1C2027",
  backgroundSubtle: "#2E333F",
  textPrimary: "#E3E5EA",
  textSecondary: "rgba(227, 229, 234, 0.66)",
  borderControl: "rgba(255, 255, 255, 0.36)",
};
