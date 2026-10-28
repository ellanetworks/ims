import { useEffect, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { Box, CircularProgress, TextField } from "@mui/material";
import AuthLayout, { AuthFrame } from "@/components/AuthLayout";
import { useSnackbar } from "@/contexts/SnackbarContext";
import PasswordField from "@/components/PasswordField";
import { login, refresh } from "@/queries/auth";
import { getStatus } from "@/queries/status";
import { decodeAccessToken } from "@/utils/jwt";

export default function Login() {
  const navigate = useNavigate();
  const location = useLocation();
  const from = (location.state as { from?: string } | null)?.from ?? "/";

  const [checking, setChecking] = useState(true);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [touched, setTouched] = useState({ email: false, password: false });
  const [submitting, setSubmitting] = useState(false);
  const { showSnackbar } = useSnackbar();

  // A system without users goes to its initialization, and a user with a session goes on.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const status = await getStatus();
        if (!status.initialized) {
          navigate("/initialize", { replace: true });
          return;
        }
        const { token } = await refresh();
        if (!decodeAccessToken(token)) throw new Error("invalid access token");
        if (!cancelled) navigate(from, { replace: true });
      } catch {
        if (!cancelled) setChecking(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [navigate, from]);

  const submit = async () => {
    setSubmitting(true);
    try {
      await login(email, password);
      navigate(from, { replace: true });
    } catch (err) {
      showSnackbar(
        err instanceof Error ? err.message : "Login failed.",
        "error",
      );
      setSubmitting(false);
    }
  };

  if (checking) {
    return (
      <AuthFrame>
        <Box>
          <CircularProgress aria-label="Loading" />
        </Box>
      </AuthFrame>
    );
  }

  return (
    <AuthLayout
      title="Login"
      submitLabel="Login"
      submitting={submitting}
      valid={email !== "" && password !== ""}
      onSubmit={submit}
    >
      <TextField
        label="Email"
        type="email"
        value={email}
        onChange={(e) => setEmail(e.target.value.trim())}
        onBlur={() => setTouched((t) => ({ ...t, email: true }))}
        error={touched.email && email === ""}
        helperText={
          touched.email && email === "" ? "Enter your email." : undefined
        }
        fullWidth
        required
        autoFocus
        autoComplete="email"
      />
      <PasswordField
        label="Password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        onBlur={() => setTouched((t) => ({ ...t, password: true }))}
        error={touched.password && password === ""}
        helperText={
          touched.password && password === ""
            ? "Enter your password."
            : undefined
        }
        fullWidth
        required
        autoComplete="current-password"
      />
    </AuthLayout>
  );
}
