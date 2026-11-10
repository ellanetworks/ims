import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  Box,
  Button,
  CircularProgress,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import { ArrowForward as ArrowForwardIcon } from "@mui/icons-material";
import AuthLayout, { AuthFrame } from "@/components/AuthLayout";
import { useSnackbar } from "@/contexts/SnackbarContext";
import PasswordField from "@/components/PasswordField";
import { initialize } from "@/queries/auth";
import { getStatus, type Formation } from "@/queries/status";
import { passwordError } from "@/queries/users";

const EMAIL = /^[^\s@]+@[^\s@]+$/;

const FORMATION_POLL_MS = 2000;

const waiting = (f?: Formation) => f !== undefined && f.state !== "member";

function FormationPanel({ formation }: { formation: Formation }) {
  if (formation.state === "removed") {
    return (
      <AuthFrame>
        <Box sx={{ textAlign: "center", maxWidth: 480 }}>
          <Typography variant="h5" component="h1" gutterBottom>
            Removed from the cluster
          </Typography>
          <Typography color="text.secondary">{formation.detail}</Typography>
        </Box>
      </AuthFrame>
    );
  }

  const count =
    formation.expected && formation.state === "forming"
      ? ` ${formation.found ?? 0}/${formation.expected}`
      : "";
  const title = formation.state === "joining" ? "Joining" : `Forming${count}`;

  return (
    <AuthFrame>
      <Box sx={{ textAlign: "center", maxWidth: 480 }}>
        <CircularProgress aria-label="Forming the cluster" sx={{ mb: 2 }} />
        <Typography variant="h5" component="h1" gutterBottom>
          {title}
        </Typography>
        {formation.detail && (
          <Typography color="text.secondary">{formation.detail}</Typography>
        )}
      </Box>
    </AuthFrame>
  );
}

export default function Initialize() {
  const navigate = useNavigate();

  const [checking, setChecking] = useState(true);
  const [clustered, setClustered] = useState(false);
  const [formation, setFormation] = useState<Formation | undefined>();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [touched, setTouched] = useState({ email: false, password: false });
  const [submitting, setSubmitting] = useState(false);
  const { showSnackbar } = useSnackbar();

  useEffect(() => {
    let cancelled = false;
    const poller: { timer?: ReturnType<typeof setTimeout> } = {};
    const check = async () => {
      let poll = false;
      try {
        const status = await getStatus();
        if (status.initialized) {
          navigate("/login", { replace: true });
          return;
        }
        if (cancelled) return;
        setClustered(status.clustered);
        setFormation(status.formation);
        poll =
          waiting(status.formation) && status.formation?.state !== "removed";
      } catch {}
      if (cancelled) return;
      setChecking(false);
      if (poll) poller.timer = setTimeout(check, FORMATION_POLL_MS);
    };
    check();
    return () => {
      cancelled = true;
      clearTimeout(poller.timer);
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

  if (formation && waiting(formation)) {
    return <FormationPanel formation={formation} />;
  }

  return (
    <AuthLayout
      title="Initialize Ella IMS"
      intro="Create the first user, an admin."
      submitLabel="Create"
      submitting={submitting}
      valid={!emailError && !newPasswordError}
      onSubmit={submit}
      footer={
        <Tooltip
          title={
            clustered
              ? ""
              : "This node has no cluster block in its configuration file."
          }
        >
          <Box component="span" sx={{ display: "block" }}>
            <Button
              fullWidth
              endIcon={<ArrowForwardIcon />}
              onClick={() => navigate("/initialize/join")}
              disabled={submitting || !clustered}
            >
              Join an existing cluster instead
            </Button>
          </Box>
        </Tooltip>
      }
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
