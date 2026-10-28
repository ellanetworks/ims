import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Box, CircularProgress, TextField } from "@mui/material";
import AuthLayout, { AuthFrame } from "@/components/AuthLayout";
import { useSnackbar } from "@/contexts/SnackbarContext";
import PasswordField from "@/components/PasswordField";
import { initialize } from "@/queries/auth";
import { getStatus } from "@/queries/status";
import { passwordError } from "@/queries/users";

const EMAIL = /^[^\s@]+@[^\s@]+$/;

export default function Initialize() {
  const navigate = useNavigate();

  const [checking, setChecking] = useState(true);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [touched, setTouched] = useState({ email: false, password: false });
  const [submitting, setSubmitting] = useState(false);
  const { showSnackbar } = useSnackbar();

  // An initialized system has its users log in.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const status = await getStatus();
        if (status.initialized) {
          navigate("/login", { replace: true });
          return;
        }
      } catch {
        // The form shows the error of the submission, if the server stays unreachable.
      }
      if (!cancelled) setChecking(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [navigate]);

  const emailError = EMAIL.test(email) ? undefined : "Enter an email address.";
  const newPasswordError = passwordError(password);

  const submit = async () => {
    setSubmitting(true);
    try {
      await initialize(email, password);
      navigate("/", { replace: true });
    } catch (err) {
      showSnackbar(
        err instanceof Error ? err.message : "Initialization failed.",
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
      title="Initialize Ella IMS"
      intro="Create the first user, an admin."
      submitLabel="Create"
      submitting={submitting}
      valid={!emailError && !newPasswordError}
      onSubmit={submit}
    >
      <TextField
        label="Email"
        type="email"
        value={email}
        onChange={(e) => setEmail(e.target.value.trim())}
        onBlur={() => setTouched((t) => ({ ...t, email: true }))}
        error={touched.email && emailError !== undefined}
        helperText={touched.email ? emailError : undefined}
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
        error={touched.password && newPasswordError !== undefined}
        helperText={touched.password ? newPasswordError : "8 to 72 characters."}
        fullWidth
        required
        autoComplete="new-password"
      />
    </AuthLayout>
  );
}
