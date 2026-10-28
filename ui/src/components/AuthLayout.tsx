import { useEffect, type FormEvent, type ReactNode } from "react";
import {
  AppBar,
  Box,
  Button,
  CircularProgress,
  Toolbar,
  Typography,
} from "@mui/material";
import Logo from "@/components/Logo";

// AuthLayout is the page of the login and of the initialization: the top bar, and a card centered under it.
export default function AuthLayout({
  title,
  intro,
  submitLabel,
  submitting,
  valid,
  onSubmit,
  children,
}: {
  title: string;
  intro?: string;
  submitLabel: string;
  submitting: boolean;
  valid: boolean;
  onSubmit: () => void;
  children: ReactNode;
}) {
  useEffect(() => {
    document.title = `${title} · Ella IMS`;
  }, [title]);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (valid && !submitting) onSubmit();
  };

  return (
    <AuthFrame>
      <Box
        component="form"
        onSubmit={submit}
        noValidate
        aria-labelledby="auth-title"
        sx={{
          width: "100%",
          maxWidth: 360,
          display: "flex",
          flexDirection: "column",
          gap: 2,
          border: "1px solid",
          borderColor: "divider",
          borderRadius: 2,
          p: 3,
          boxShadow: 2,
          bgcolor: "background.paper",
        }}
      >
        <Typography
          id="auth-title"
          variant="h5"
          component="h1"
          sx={{ textAlign: "center" }}
        >
          {title}
        </Typography>
        {intro && <Typography variant="body1">{intro}</Typography>}
        {children}
        <Button
          type="submit"
          variant="contained"
          color="success"
          fullWidth
          disabled={!valid || submitting}
        >
          {submitting ? <CircularProgress size={24} /> : submitLabel}
        </Button>
      </Box>
    </AuthFrame>
  );
}

// AuthFrame is the top bar, and its content centered under it.
export function AuthFrame({ children }: { children: ReactNode }) {
  return (
    <Box sx={{ display: "flex", flexDirection: "column", minHeight: "100vh" }}>
      <AppBar position="fixed">
        <Toolbar>
          <Logo />
          <Typography variant="h6" noWrap component="div" sx={{ ml: 2 }}>
            Ella IMS
          </Typography>
        </Toolbar>
      </AppBar>
      <Toolbar />
      <Box
        component="main"
        sx={{
          flexGrow: 1,
          display: "flex",
          justifyContent: "center",
          alignItems: "center",
          p: 2,
        }}
      >
        {children}
      </Box>
    </Box>
  );
}
