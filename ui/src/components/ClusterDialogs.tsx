import { useState } from "react";
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { LeaderLink } from "@/components/ClusterStateCard";
import ConfirmDialog from "@/components/ConfirmDialog";
import CopyButton from "@/components/CopyButton";
import DialogError from "@/components/DialogError";
import EditDialog from "@/components/EditDialog";
import { useSnackbar } from "@/contexts/SnackbarContext";
import {
  createJoinToken,
  drainMember,
  MAX_DISPLAY_NAME_LENGTH,
  memberLabel,
  removeMember,
  resumeMember,
  updateDisplayName,
  type ClusterMember,
} from "@/queries/cluster";
import { ApiError } from "@/queries/utils";
import { formatDateTime } from "@/utils/dates";

export const TTL_OPTIONS: { label: string; seconds: number }[] = [
  { label: "15 minutes", seconds: 15 * 60 },
  { label: "30 minutes", seconds: 30 * 60 },
  { label: "1 hour", seconds: 60 * 60 },
  { label: "4 hours", seconds: 4 * 60 * 60 },
  { label: "24 hours", seconds: 24 * 60 * 60 },
];

const DEFAULT_TTL = 30 * 60;

const useClusterMutation = <T,>(
  mutationFn: (value: T) => Promise<unknown>,
  success: string,
  onClose: () => void,
) => {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();

  return useMutation({
    mutationFn,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["cluster"] });
      showSnackbar(success, "success");
      onClose();
    },
  });
};

export function NodeIdentity({ nodeId }: { nodeId: string }) {
  return (
    <Box sx={{ mt: 2 }}>
      <Typography variant="caption" sx={{ color: "text.secondary" }}>
        Node identity
      </Typography>
      <Typography
        variant="body2"
        sx={{ fontFamily: "monospace", wordBreak: "break-all" }}
      >
        {nodeId}
      </Typography>
    </Box>
  );
}

export function LeaderError({
  error,
  prefix,
}: {
  error: Error | null;
  prefix: string;
}) {
  const leader = error instanceof ApiError ? error.leader : undefined;

  return (
    <Stack spacing={1}>
      <DialogError error={error} prefix={prefix} />
      {leader && (
        <Typography variant="body2">
          Only the leader can do this. Open the leader&apos;s UI at{" "}
          <LeaderLink address={leader} />
        </Typography>
      )}
    </Stack>
  );
}

function CopyBlock({ label, value }: { label: string; value: string }) {
  return (
    <Box>
      <Typography variant="caption" color="textSecondary">
        {label}
      </Typography>
      <Box
        sx={{
          position: "relative",
          mt: 0.5,
          p: 1.5,
          pr: 5,
          border: 1,
          borderColor: "divider",
          borderRadius: 1,
          bgcolor: "background.default",
        }}
      >
        <Typography
          component="pre"
          variant="body2"
          data-testid="join-token"
          sx={{
            whiteSpace: "pre-wrap",
            wordBreak: "break-all",
            fontFamily: "monospace",
            userSelect: "all",
            m: 0,
          }}
        >
          {value}
        </Typography>
        <Box sx={{ position: "absolute", top: 4, right: 4 }}>
          <CopyButton value={value} label={label.toLowerCase()} />
        </Box>
      </Box>
    </Box>
  );
}

export function AddNodeDialog({ onClose }: { onClose: () => void }) {
  const [ttlSeconds, setTtlSeconds] = useState(DEFAULT_TTL);
  const mint = useMutation({ mutationFn: createJoinToken });
  const token = mint.data;

  return (
    <Dialog open onClose={onClose} fullWidth maxWidth="sm">
      <DialogTitle>Add a Node to the Cluster</DialogTitle>
      <DialogContent dividers>
        <Stack spacing={2}>
          <DialogError
            error={mint.error}
            prefix="Could not create the join token"
          />
          {!token && (
            <>
              <Typography variant="body2">
                This creates a single-use token that lets one new node join the
                cluster. The new node needs a cluster block in its configuration
                file, and no user yet.
              </Typography>
              <TextField
                select
                autoFocus
                label="Token lifetime"
                value={ttlSeconds}
                onChange={(e) => setTtlSeconds(Number(e.target.value))}
              >
                {TTL_OPTIONS.map((opt) => (
                  <MenuItem key={opt.seconds} value={opt.seconds}>
                    {opt.label}
                  </MenuItem>
                ))}
              </TextField>
            </>
          )}
          {token && (
            <>
              <Typography variant="body2" color="success.main">
                Token created. Copy it now, because it is shown only once. It
                expires on {formatDateTime(token.expires_at)}.
              </Typography>
              <Typography variant="body2">
                Open the new node in a browser, click{" "}
                <strong>Join an existing cluster instead</strong> and paste this
                token.
              </Typography>
              <CopyBlock label="Join token" value={token.token} />
            </>
          )}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} disabled={mint.isPending}>
          {token ? "Close" : "Cancel"}
        </Button>
        {!token && (
          <Button
            variant="contained"
            color="success"
            onClick={() => mint.mutate(ttlSeconds)}
            disabled={mint.isPending}
          >
            {mint.isPending ? "Creating…" : "Create Token"}
          </Button>
        )}
      </DialogActions>
    </Dialog>
  );
}

export function DrainNodeDialog({
  member,
  isLastActive,
  isSelf,
  onClose,
}: {
  member: ClusterMember;
  isLastActive: boolean;
  isSelf: boolean;
  onClose: () => void;
}) {
  const label = memberLabel(member);
  const drain = useClusterMutation(
    () => drainMember(member.node_id),
    `Node ${label} is draining.`,
    onClose,
  );

  return (
    <ConfirmDialog
      title={`Drain node ${label}?`}
      description={
        <>
          <strong>Node {label}</strong> fails readiness, and its P-CSCF answers
          REGISTER with 305 Use Proxy, so UEs register through the rest of the
          cluster.
        </>
      }
      confirmLabel="Drain"
      pendingLabel="Draining…"
      confirmColor="warning"
      maxWidth="sm"
      pending={drain.isPending}
      onConfirm={() => drain.mutate(undefined)}
      onClose={onClose}
    >
      <NodeIdentity nodeId={member.node_id} />
      <Stack spacing={1} sx={{ mt: 2 }}>
        <DialogError error={drain.error} prefix="Could not drain the node" />
        {isLastActive && (
          <Alert severity="warning">
            No other node is active, so UEs have no node left to register
            through.
          </Alert>
        )}
        {member.leader && (
          <Alert severity="info">
            This node is the leader. Leadership will move to another node.
          </Alert>
        )}
        {isSelf && (
          <Alert severity="info">
            This is the node serving this page. It stops taking new
            registrations; your session is unaffected.
          </Alert>
        )}
      </Stack>
    </ConfirmDialog>
  );
}

export function ResumeNodeDialog({
  member,
  onClose,
}: {
  member: ClusterMember;
  onClose: () => void;
}) {
  const label = memberLabel(member);
  const resume = useClusterMutation(
    () => resumeMember(member.node_id),
    `Node ${label} resumed.`,
    onClose,
  );

  return (
    <ConfirmDialog
      title={`Resume node ${label}?`}
      description={
        <>
          <strong>Node {label}</strong> accepts registrations again and passes
          readiness.
        </>
      }
      confirmLabel="Resume"
      pendingLabel="Resuming…"
      confirmColor="primary"
      maxWidth="sm"
      pending={resume.isPending}
      onConfirm={() => resume.mutate(undefined)}
      onClose={onClose}
    >
      <NodeIdentity nodeId={member.node_id} />
      <Box sx={{ mt: 2 }}>
        <DialogError error={resume.error} prefix="Could not resume the node" />
      </Box>
    </ConfirmDialog>
  );
}

export function RemoveNodeDialog({
  member,
  isSelf,
  onClose,
}: {
  member: ClusterMember;
  isSelf: boolean;
  onClose: () => void;
}) {
  const label = memberLabel(member);
  const forceRequired = member.drain_state !== "drained";
  const [force, setForce] = useState(forceRequired && member.healthy === false);
  const remove = useClusterMutation(
    () => removeMember(member.node_id, forceRequired && force),
    `Node ${label} removed.`,
    onClose,
  );

  return (
    <ConfirmDialog
      title={`Remove node ${label}?`}
      description={
        <>
          <strong>Node {label}</strong> closes its listeners, fails readiness,
          and refuses to start until its data directory is deleted.
        </>
      }
      confirmLabel="Remove"
      pendingLabel="Removing…"
      maxWidth="sm"
      pending={remove.isPending}
      confirmDisabled={forceRequired && !force}
      onConfirm={() => remove.mutate(undefined)}
      onClose={onClose}
    >
      <NodeIdentity nodeId={member.node_id} />
      <Box sx={{ mt: 2 }}>
        <LeaderError error={remove.error} prefix="Could not remove the node" />
        {forceRequired && (
          <>
            <FormControlLabel
              control={
                <Checkbox
                  checked={force}
                  onChange={(e) => setForce(e.target.checked)}
                />
              }
              label="Force remove (skip drain)"
            />
            <Typography variant="body2" color="textSecondary">
              {member.healthy === false
                ? "This node is unreachable, so it cannot be drained first."
                : "This node is not drained: UEs registered through it lose service until they register again. To move them gracefully, cancel and drain the node before removing it."}
            </Typography>
          </>
        )}
        {isSelf && (
          <Typography variant="body2" color="warning.main" sx={{ mt: 1 }}>
            This is the node serving this page. Removing it ends your session on
            this node.
          </Typography>
        )}
      </Box>
    </ConfirmDialog>
  );
}

export function RenameNodeDialog({
  member,
  onClose,
}: {
  member: ClusterMember;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [displayName, setDisplayName] = useState(member.display_name);
  const rename = useClusterMutation(
    async (value: string) => {
      await updateDisplayName(member.node_id, value);
      void queryClient.invalidateQueries({ queryKey: ["status"] });
    },
    "Display name updated.",
    onClose,
  );
  const name = displayName.trim();

  return (
    <EditDialog
      title="Rename node"
      valid={
        name !== member.display_name && name.length <= MAX_DISPLAY_NAME_LENGTH
      }
      pending={rename.isPending}
      error={rename.error}
      onSubmit={() => rename.mutate(name)}
      onClose={onClose}
      submitLabel="Save"
      pendingLabel="Saving…"
      failure="Could not set the display name"
    >
      <Typography variant="body2">
        {`Sets the name shown for node ${memberLabel(member)} across the UI.`}
      </Typography>
      <TextField
        label="Display name"
        value={displayName}
        onChange={(e) => setDisplayName(e.target.value)}
        helperText={`Leave empty to show the Diameter host label ${member.diameter_label} instead (max ${MAX_DISPLAY_NAME_LENGTH} characters).`}
        autoFocus
        slotProps={{ htmlInput: { maxLength: MAX_DISPLAY_NAME_LENGTH } }}
      />
      <NodeIdentity nodeId={member.node_id} />
    </EditDialog>
  );
}
