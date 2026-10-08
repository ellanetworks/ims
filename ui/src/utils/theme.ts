import { createTheme } from "@mui/material/styles";
import { dark, light, type Tokens } from "@/utils/tokens";

const paletteFor = (tokens: Tokens) => ({
  contrastThreshold: 4.5,
  primary: { main: tokens.primary },
  success: { main: tokens.success },
  error: { main: tokens.error },
  warning: { main: tokens.warning },
  info: { main: tokens.info },
  background: {
    default: tokens.backgroundDefault,
    paper: tokens.backgroundPaper,
  },
  text: {
    primary: tokens.textPrimary,
    secondary: tokens.textSecondary,
  },
});

const theme = createTheme({
  cssVariables: { colorSchemeSelector: "media" },
  colorSchemes: {
    light: { palette: { mode: "light", ...paletteFor(light) } },
    dark: { palette: { mode: "dark", ...paletteFor(dark) } },
  },
  typography: {
    fontFamily: '"Source Code Pro", monospace',
  },
});

export default theme;
