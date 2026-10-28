import { useState, type ReactNode } from "react";
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Checkbox,
  Chip,
  FormControlLabel,
  IconButton,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import {
  Close as CloseIcon,
  ContentCopy as ContentCopyIcon,
  Delete as DeleteIcon,
  Edit as EditIcon,
} from "@mui/icons-material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import ConfirmDialog from "@/components/ConfirmDialog";
import { errorMessage, useSnackbar } from "@/contexts/SnackbarContext";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import EditDialog from "@/components/EditDialog";
import {
  createAPIToken,
  deleteAPIToken,
  MAX_API_TOKENS,
  type APIToken,
} from "@/queries/api_tokens";
import { RoleID, roleLabels, type User } from "@/queries/users";
import { formatTimestamp } from "@/utils/dates";
import { TABLE_CONTAINER_SX } from "@/utils/layout";

const roleChipColor = (role: RoleID): "primary" | "success" | "default" => {
  switch (role) {
    case RoleID.Admin:
      return "primary";
    case RoleID.NetworkManager:
      return "success";
    default:
      return "default";
  }
};

function InfoRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Box
      sx={{
        display: "flex",
        alignItems: "center",
        py: 0.75,
        "&:not(:last-child)": {
          borderBottom: "1px solid",
          borderColor: "divider",
        },
      }}
    >
      <Typography
        variant="body2"
        sx={{ color: "text.secondary", minWidth: 120, flexShrink: 0, mr: 2 }}
      >
        {label}
      </Typography>
      {children}
    </Box>
  );
}

export function UserAccountCard({
  user,
  onEditRole,
}: {
  user: User;
  onEditRole?: () => void;
}) {
  return (
    <Card variant="outlined" sx={{ height: "100%" }}>
      <CardContent>
        <Typography variant="h6" component="h2" sx={{ mb: 1.5 }}>
          Account
        </Typography>
        <InfoRow label="Email">
          <Typography variant="body2" sx={{ overflowWrap: "anywhere" }}>
            {user.email}
          </Typography>
        </InfoRow>
        <InfoRow label="Role">
          <Chip
            size="small"
            label={roleLabels[user.role_id]}
            color={roleChipColor(user.role_id)}
            variant="outlined"
          />
          <Box sx={{ flex: 1 }} />
          {onEditRole && (
            <IconButton
              size="small"
              onClick={onEditRole}
              aria-label="Edit role"
            >
              <EditIcon fontSize="small" color="primary" />
            </IconButton>
          )}
        </InfoRow>
      </CardContent>
    </Card>
  );
}

export function UserPasswordCard({
  description,
  onChangePassword,
  disabledReason,
}: {
  description: string;
  onChangePassword: () => void;
  disabledReason?: string;
}) {
  return (
    <Card variant="outlined" sx={{ height: "100%" }}>
      <CardContent>
        <Typography variant="h6" component="h2" sx={{ mb: 1.5 }}>
          Password
        </Typography>
        <Typography variant="body2" color="textSecondary" sx={{ mb: 2 }}>
          {disabledReason ?? description}
        </Typography>
        <Tooltip title={disabledReason ?? ""}>
          <span>
            <Button
              variant="contained"
              color="success"
              onClick={onChangePassword}
              disabled={disabledReason !== undefined}
            >
              Change Password
            </Button>
          </span>
        </Tooltip>
      </CardContent>
    </Card>
  );
}

// tokenQueryKey is the key of the API tokens of the logged-in user when email is undefined, else of that user.
export const tokenQueryKey = (email?: string) =>
  email === undefined ? ["apiTokens", "me"] : ["apiTokens", "user", email];

// UserAPITokensCard lists the API tokens of the logged-in user when email is undefined, else of that user, and creates
// and deletes them.
export function UserAPITokensCard({
  tokens,
  email,
}: {
  tokens: APIToken[];
  email?: string;
}) {
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<APIToken | null>(null);
  const { showSnackbar } = useSnackbar();
  const copy = useCopyToClipboard();
  // The time the expiry of the tokens is shown against.
  const [now] = useState(() => Date.now());

  const remove = useMutation({
    mutationFn: (token: APIToken) => deleteAPIToken(token.id, email),
    onSuccess: (_result, token) => {
      void queryClient.invalidateQueries({ queryKey: tokenQueryKey(email) });
      showSnackbar(
        `API token "${token.name}" deleted successfully.`,
        "success",
      );
    },
    onError: (error) =>
      showSnackbar(
        `Failed to delete API token: ${errorMessage(error)}`,
        "error",
      ),
    onSettled: () => setDeleting(null),
  });

  const full = tokens.length >= MAX_API_TOKENS;

  return (
    <>
      <Card variant="outlined">
        <CardContent>
          <Box
            sx={{
              display: "flex",
              alignItems: "center",
              justifyContent: "space-between",
              mb: 2,
            }}
          >
            <Typography variant="h6" component="h2">
              API Tokens
            </Typography>
            <Tooltip
              title={full ? `At most ${MAX_API_TOKENS} API tokens.` : ""}
            >
              <span>
                <Button
                  variant="contained"
                  color="success"
                  size="small"
                  onClick={() => {
                    setCreated(null);
                    setCreating(true);
                  }}
                  disabled={full}
                >
                  Create Token
                </Button>
              </span>
            </Tooltip>
          </Box>

          <Typography variant="body2" color="textSecondary" sx={{ mb: 2 }}>
            {email === undefined
              ? "An API token calls the API with your permissions."
              : "An API token calls the API with this user's permissions."}
            {full &&
              ` At most ${MAX_API_TOKENS} API tokens: delete one to create another.`}
          </Typography>

          {created && (
            <Alert
              severity="success"
              variant="outlined"
              sx={{ alignItems: "center", mb: 2 }}
              action={
                <Stack direction="row" spacing={1}>
                  <IconButton
                    aria-label="Copy token"
                    title="Copy"
                    size="small"
                    onClick={() => void copy(created, "API token")}
                  >
                    <ContentCopyIcon fontSize="inherit" />
                  </IconButton>
                  <IconButton
                    aria-label="Dismiss"
                    size="small"
                    onClick={() => setCreated(null)}
                  >
                    <CloseIcon fontSize="inherit" />
                  </IconButton>
                </Stack>
              }
            >
              <Typography variant="body2" sx={{ mb: 0.5 }}>
                Copy the token now. It is not shown again.
              </Typography>
              <Typography
                variant="body2"
                data-testid="new-api-token"
                sx={{ wordBreak: "break-all", userSelect: "all" }}
              >
                {created}
              </Typography>
            </Alert>
          )}

          <TableContainer sx={TABLE_CONTAINER_SX}>
            <Table aria-label="API tokens">
              <TableHead>
                <TableRow>
                  <TableCell>Name</TableCell>
                  <TableCell>Status</TableCell>
                  <TableCell>Expiry</TableCell>
                  <TableCell>Last used</TableCell>
                  <TableCell align="right">Actions</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {tokens.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={5}>
                      <Typography variant="body2" color="textSecondary">
                        No API tokens.
                      </Typography>
                    </TableCell>
                  </TableRow>
                )}
                {tokens.map((token) => {
                  const expired =
                    token.expires_at !== undefined &&
                    new Date(token.expires_at).getTime() <= now;
                  return (
                    <TableRow key={token.id}>
                      <TableCell>{token.name}</TableCell>
                      <TableCell>
                        {expired ? (
                          <Chip label="Expired" color="warning" size="small" />
                        ) : (
                          <Chip label="Active" color="success" size="small" />
                        )}
                      </TableCell>
                      <TableCell>
                        {token.expires_at
                          ? formatTimestamp(token.expires_at)
                          : "Never"}
                      </TableCell>
                      <TableCell>
                        {token.last_used_at
                          ? formatTimestamp(token.last_used_at)
                          : "Never"}
                      </TableCell>
                      <TableCell align="right">
                        <IconButton
                          aria-label={`Delete ${token.name}`}
                          size="small"
                          onClick={() => setDeleting(token)}
                        >
                          <DeleteIcon fontSize="small" color="primary" />
                        </IconButton>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </TableContainer>
        </CardContent>
      </Card>

      {creating && (
        <CreateAPITokenDialog
          email={email}
          onClose={() => setCreating(false)}
          onCreated={(token) => {
            setCreating(false);
            setCreated(token);
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete API Token"
          description={`Delete the API token "${deleting.name}"? The programs that use it can no longer call the API.`}
          confirmLabel="Delete"
          pending={remove.isPending}
          onConfirm={() => remove.mutate(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </>
  );
}

// localDate is a date of the browser's time zone as YYYY-MM-DD, the value of a date input.
export const localDate = (d: Date) =>
  [
    d.getFullYear(),
    String(d.getMonth() + 1).padStart(2, "0"),
    String(d.getDate()).padStart(2, "0"),
  ].join("-");

// endOfDay is the end of a YYYY-MM-DD date in the browser's time zone: the start of the next day.
export const endOfDay = (date: string) => {
  const [year, month, day] = date.split("-").map(Number);
  return new Date(year, month - 1, day + 1);
};

function CreateAPITokenDialog({
  email,
  onClose,
  onCreated,
}: {
  email?: string;
  onClose: () => void;
  onCreated: (token: string) => void;
}) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [noExpiry, setNoExpiry] = useState(false);
  const [expiry, setExpiry] = useState("");
  const [min] = useState(() => localDate(new Date()));

  const mutation = useMutation({
    // The token works through its expiry date, in the browser's time zone.
    mutationFn: () =>
      createAPIToken(
        name.trim(),
        noExpiry ? undefined : endOfDay(expiry).toISOString(),
        email,
      ),
    onSuccess: ({ token }) => {
      void queryClient.invalidateQueries({ queryKey: tokenQueryKey(email) });
      onCreated(token);
    },
  });

  const nameLength = name.trim().length;
  const nameError =
    name === "" || (nameLength >= 3 && nameLength <= 50)
      ? undefined
      : "3 to 50 characters.";

  return (
    <EditDialog
      title="Create API Token"
      valid={
        nameLength >= 3 &&
        nameLength <= 50 &&
        (noExpiry || (expiry !== "" && expiry >= min))
      }
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
      submitLabel="Create"
      pendingLabel="Creating…"
      failure="Could not create the API token"
    >
      <TextField
        label="Name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        error={nameError !== undefined}
        helperText={nameError ?? "What uses it, such as your monitoring."}
        required
        autoFocus
      />
      <TextField
        label="Expiry date"
        type="date"
        value={expiry}
        onChange={(e) => setExpiry(e.target.value)}
        disabled={noExpiry}
        required={!noExpiry}
        helperText="The token works through this day."
        slotProps={{ inputLabel: { shrink: true }, htmlInput: { min } }}
      />
      <FormControlLabel
        control={
          <Checkbox
            checked={noExpiry}
            onChange={(e) => setNoExpiry(e.target.checked)}
          />
        }
        label="No expiry"
      />
    </EditDialog>
  );
}
