import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  IconButton,
  Link,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import {
  ArrowBack as ArrowBackIcon,
  CheckCircle as CheckCircleIcon,
  Close as CloseIcon,
  OpenInNew as OpenInNewIcon,
} from "@mui/icons-material";
import AuthLayout, { AuthFrame } from "@/components/AuthLayout";
import { errorMessage, useSnackbar } from "@/contexts/SnackbarContext";
import { joinCluster } from "@/queries/cluster";
import { getStatus } from "@/queries/status";
import { CONFIG_DOCS_URL } from "@/utils/docs";

export const JOIN_POLL_MS = 1000;

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function waitForData() {
  for (;;) {
    try {
      if ((await getStatus()).initialized) return;
    } catch {}
    await sleep(JOIN_POLL_MS);
  }
}

export default function ClusterJoin() {
  const navigate = useNavigate();
  const { showSnackbar } = useSnackbar();
  const [checking, setChecking] = useState(true);
  const [clustered, setClustered] = useState(false);
  const [token, setToken] = useState("");
  const [editingToken, setEditingToken] = useState(true);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const status = await getStatus();
        if (status.initialized) {
          navigate("/login", { replace: true });
          return;
        }
        if (!cancelled) setClustered(status.clustered);
      } catch {}
      if (!cancelled) setChecking(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [navigate]);

  const commitToken = (value: string) => {
    const trimmed = value.trim();
    setToken(trimmed);
    if (trimmed !== "") setEditingToken(false);
  };

  const clearToken = () => {
    setToken("");
    setEditingToken(true);
  };

  const submit = async () => {
    setSubmitting(true);
    try {
      await joinCluster(token.trim());
      await waitForData();
      showSnackbar(
        "Joined the cluster. Log in with a user of the cluster.",
        "success",
      );
      navigate("/login", { replace: true });
    } catch (err) {
      showSnackbar(`Failed to join the cluster: ${errorMessage(err)}`, "error");
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
      title="Join a cluster"
      intro="Join this node to an existing high-availability cluster."
      submitLabel="Join"
      submitting={submitting}
      valid={clustered && token.trim() !== ""}
      onSubmit={() => void submit()}
      footer={
        <Button
          fullWidth
          startIcon={<ArrowBackIcon />}
          disabled={submitting}
          onClick={() => navigate("/initialize")}
        >
          Back
        </Button>
      }
    >
      {!clustered && (
        <Alert severity="info">
          This node has no cluster address. Add a cluster block to its
          configuration file and restart it to join a cluster.{" "}
          <Link
            href={CONFIG_DOCS_URL}
            target="_blank"
            rel="noreferrer"
            underline="hover"
            sx={{ display: "inline-flex", alignItems: "center" }}
          >
            Learn more
            <OpenInNewIcon sx={{ fontSize: 16, ml: 0.5 }} />
          </Link>
        </Alert>
      )}
      {clustered && (
        <Typography variant="body2" color="textSecondary">
          Create a join token on the Cluster page of a node already in the
          cluster. This node replaces its data with the cluster&apos;s; log in
          with a user of the cluster afterward.
        </Typography>
      )}
      {editingToken ? (
        <TextField
          fullWidth
          label="Join token"
          placeholder="Paste the join token"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          onPaste={(e) => {
            const pasted = e.clipboardData.getData("text").trim();
            if (!pasted) return;
            e.preventDefault();
            commitToken(pasted);
          }}
          onBlur={() => commitToken(token)}
          disabled={submitting || !clustered}
          required
          autoFocus={clustered}
        />
      ) : (
        <Box>
          <Typography variant="caption" color="textSecondary">
            Join token
          </Typography>
          <Stack
            direction="row"
            spacing={1}
            sx={{
              alignItems: "center",
              border: "1px solid",
              borderColor: "divider",
              borderRadius: 1,
              px: 1.5,
              py: 1,
              mt: 0.5,
            }}
          >
            <CheckCircleIcon color="success" fontSize="small" />
            <Typography variant="body2" sx={{ flexGrow: 1 }}>
              Token pasted · ends in {token.slice(-4)}
            </Typography>
            <IconButton
              size="small"
              aria-label="Clear join token"
              disabled={submitting}
              onClick={clearToken}
            >
              <CloseIcon fontSize="small" />
            </IconButton>
          </Stack>
        </Box>
      )}
      {submitting && (
        <Typography variant="body2" component="output">
          Joining the cluster. This node is contacting the cluster and copying
          its data, which can take a minute.
        </Typography>
      )}
    </AuthLayout>
  );
}
