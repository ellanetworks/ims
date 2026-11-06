import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router-dom";
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Divider,
  TextField,
} from "@mui/material";
import AuthLayout, { AuthFrame } from "@/components/AuthLayout";
import { useSnackbar } from "@/contexts/SnackbarContext";
import PasswordField from "@/components/PasswordField";
import { login, refresh } from "@/queries/auth";
import {
  getOIDCLogin,
  OIDC_LOGIN_PATH,
  oidcLoginError,
  type OIDCLogin,
} from "@/queries/oidc";
import { getStatus } from "@/queries/status";
import { decodeAccessToken } from "@/utils/jwt";
import {
  forgetReturnPath,
  rememberReturnPath,
  returnPath,
} from "@/utils/returnPath";

export default function Login() {
  const navigate = useNavigate();
  const location = useLocation();
  const [from] = useState(
    () =>
      (location.state as { from?: string } | null)?.from ?? returnPath() ?? "/",
  );
  const [params, setParams] = useSearchParams();
  const [oidcError] = useState(() => params.get("error"));

  useEffect(() => {
    if (params.has("error")) {
      setParams(
        (current) => {
          const next = new URLSearchParams(current);
          next.delete("error");
          return next;
        },
        { replace: true, state: location.state },
      );
    }
  }, [params, setParams, location.state]);

  const [checking, setChecking] = useState(true);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [touched, setTouched] = useState({ email: false, password: false });
  const [submitting, setSubmitting] = useState(false);
  const [oidc, setOIDC] = useState<OIDCLogin>({ enabled: false });
  const { showSnackbar } = useSnackbar();
  const ssoButton = useRef<HTMLAnchorElement>(null);

  useLayoutEffect(() => {
    if (!checking && oidc.enabled) ssoButton.current?.focus();
  }, [checking, oidc.enabled]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const status = await getStatus();
        if (!status.initialized) {
          navigate("/initialize", { replace: true });
          return;
        }
        const oidcLogin = getOIDCLogin().catch((): OIDCLogin => ({
          enabled: false,
        }));
        const session = await refresh()
          .then(({ token }) => decodeAccessToken(token) !== null)
          .catch(() => false);
        if (cancelled) return;
        if (session) {
          forgetReturnPath();
          navigate(from, { replace: true });
          return;
        }
        const o = await oidcLogin;
        if (cancelled) return;
        setOIDC(o);
        setChecking(false);
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
      forgetReturnPath();
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
      submitVariant={oidc.enabled ? "outlined" : "contained"}
      submitting={submitting}
      valid={email !== "" && password !== ""}
      onSubmit={submit}
    >
      {oidcError && <Alert severity="error">{oidcLoginError(oidcError)}</Alert>}
      {oidc.enabled && (
        <>
          <Button
            variant="contained"
            color="success"
            fullWidth
            disableFocusRipple
            sx={{
              "&.Mui-focusVisible": {
                outline: "2px solid",
                outlineColor: "success.main",
                outlineOffset: 2,
              },
            }}
            href={OIDC_LOGIN_PATH}
            onClick={() => rememberReturnPath(from)}
            ref={ssoButton}
          >
            Log in with {oidc.display_name}
          </Button>
          <Divider>or</Divider>
        </>
      )}
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
        autoFocus={!oidc.enabled}
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
