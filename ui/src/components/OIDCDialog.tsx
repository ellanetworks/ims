import { useRef, useState } from "react";
import {
  Alert,
  Box,
  Button,
  FormControlLabel,
  IconButton,
  MenuItem,
  Stack,
  Switch,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import { Add as AddIcon, Delete as DeleteIcon } from "@mui/icons-material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import PasswordField from "@/components/PasswordField";
import RoleField from "@/components/RoleField";
import { useSnackbar } from "@/contexts/SnackbarContext";
import {
  caCertificateError,
  currentUpdate,
  groupErrors,
  httpsURLError,
  MAX_DISPLAY_NAME_BYTES,
  MAX_GROUP_ROLES,
  MAX_SESSION_LIFETIME_SECONDS,
  MAX_VALUE_BYTES,
  MIN_SESSION_LIFETIME_SECONDS,
  type OIDCSettings,
  scopesError,
  splitScopes,
  textError,
  type UpdateOIDCSettings,
  updateOIDC,
} from "@/queries/oidc";
import { RoleID } from "@/queries/users";

const lifetimeUnits = [
  { label: "days", seconds: 86400 },
  { label: "hours", seconds: 3600 },
  { label: "minutes", seconds: 60 },
  { label: "seconds", seconds: 1 },
];

const unitsFor = (seconds: number) =>
  lifetimeUnits.filter((u) => u.seconds >= 60 || seconds % 60 !== 0);

const largestUnit = (seconds: number) =>
  lifetimeUnits.find((u) => seconds % u.seconds === 0)!.seconds;

interface MappingRow {
  id: number;
  group: string;
  role_id: RoleID;
}

function useUpdateOIDC(onClose: () => void) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  return useMutation({
    mutationFn: (update: UpdateOIDCSettings) => updateOIDC(update),
    onSuccess: (updated) => {
      queryClient.setQueryData(["oidc"], updated);
      void queryClient.invalidateQueries({ queryKey: ["users"] });
      showSnackbar("Single sign-on updated successfully.", "success");
      onClose();
    },
  });
}

export function OIDCExternalURLDialog({
  settings,
  onClose,
}: {
  settings: OIDCSettings;
  onClose: () => void;
}) {
  const [externalURL, setExternalURL] = useState(
    settings.external_url ||
      (window.location.protocol === "https:" ? window.location.origin : ""),
  );
  const mutation = useUpdateOIDC(onClose);
  const error = httpsURLError(externalURL.trim(), {
    origin: true,
    missing: settings.enabled ? "Enter the external URL." : undefined,
  });

  return (
    <EditDialog
      title="Edit External URL"
      valid={error === undefined}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() =>
        mutation.mutate({
          ...currentUpdate(settings),
          external_url: externalURL.trim().replace(/\/$/, ""),
        })
      }
      onClose={onClose}
    >
      <TextField
        label="External URL"
        value={externalURL}
        onChange={(e) => setExternalURL(e.target.value)}
        error={error !== undefined}
        helperText={error ?? "The https address users open Ella IMS at."}
        required={settings.enabled}
        autoFocus
      />
    </EditDialog>
  );
}

export function OIDCProviderDialog({
  settings,
  onClose,
}: {
  settings: OIDCSettings;
  onClose: () => void;
}) {
  const enabled = settings.enabled;
  const [displayName, setDisplayName] = useState(settings.display_name);
  const [issuer, setIssuer] = useState(settings.issuer);
  const [clientID, setClientID] = useState(settings.client_id);
  const [clientSecret, setClientSecret] = useState("");
  const [scopes, setScopes] = useState(settings.scopes.join(" "));
  const [lifetimeUnit, setLifetimeUnit] = useState(() =>
    largestUnit(settings.session_lifetime_seconds),
  );
  const [lifetime, setLifetime] = useState(() =>
    String(settings.session_lifetime_seconds / lifetimeUnit),
  );
  const [caCertificate, setCACertificate] = useState(settings.ca_certificate);
  const [trustEmail, setTrustEmail] = useState(settings.trust_unverified_email);

  const lifetimeSeconds = /^\d+$/.test(lifetime.trim())
    ? Number(lifetime.trim()) * lifetimeUnit
    : NaN;
  const scopeList = splitScopes(scopes);
  const identityChanged =
    issuer.trim() !== settings.issuer || clientID.trim() !== settings.client_id;
  const signsOut = enabled && issuer.trim() !== settings.issuer;

  const mutation = useUpdateOIDC(onClose);

  const errors = {
    displayName: textError(
      displayName.trim(),
      MAX_DISPLAY_NAME_BYTES,
      enabled ? "Enter the name to show on the login page." : undefined,
    ),
    issuer: httpsURLError(issuer.trim(), {
      missing: enabled ? "Enter the issuer URL." : undefined,
    }),
    clientID: textError(
      clientID.trim(),
      MAX_VALUE_BYTES,
      enabled ? "Enter the client ID." : undefined,
    ),
    clientSecret:
      textError(clientSecret, MAX_VALUE_BYTES) ??
      (clientSecret !== ""
        ? undefined
        : settings.client_secret_set
          ? identityChanged
            ? "Enter the client secret again: the issuer URL or client ID changed."
            : undefined
          : enabled
            ? "Enter the client secret."
            : undefined),
    scopes: scopesError(scopeList),
    lifetime:
      !Number.isSafeInteger(lifetimeSeconds) ||
      lifetimeSeconds < MIN_SESSION_LIFETIME_SECONDS ||
      lifetimeSeconds > MAX_SESSION_LIFETIME_SECONDS
        ? "From 5 minutes to 30 days."
        : undefined,
    caCertificate: caCertificateError(caCertificate.trim()),
  };
  const valid = Object.values(errors).every((e) => e === undefined);

  return (
    <EditDialog
      title="Edit Provider"
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() =>
        mutation.mutate({
          ...currentUpdate(settings),
          issuer: issuer.trim(),
          client_id: clientID.trim(),
          client_secret: clientSecret,
          display_name: displayName.trim(),
          scopes: scopeList,
          session_lifetime_seconds: lifetimeSeconds,
          trust_unverified_email: trustEmail,
          ca_certificate: caCertificate.trim(),
        })
      }
      onClose={onClose}
      notice={
        signsOut && (
          <Alert severity="warning">
            Saving signs out everyone who logged in with single sign-on.
          </Alert>
        )
      }
    >
      <TextField
        label="Display name"
        value={displayName}
        onChange={(e) => setDisplayName(e.target.value)}
        error={errors.displayName !== undefined}
        helperText={
          errors.displayName ?? 'Shown on the login page: "Log in with …".'
        }
        required={enabled}
      />
      <TextField
        label="Issuer URL"
        value={issuer}
        onChange={(e) => setIssuer(e.target.value)}
        error={errors.issuer !== undefined}
        helperText={
          errors.issuer ?? "Such as https://idp.example.com/realms/ims."
        }
        required={enabled}
      />
      <TextField
        label="CA certificate"
        value={caCertificate}
        onChange={(e) => setCACertificate(e.target.value)}
        error={errors.caCertificate !== undefined}
        helperText={
          errors.caCertificate ??
          "Optional. Only if a private CA signs the identity provider's certificate."
        }
        multiline
        minRows={2}
        maxRows={8}
        slotProps={{ htmlInput: { style: { fontFamily: "monospace" } } }}
      />
      <TextField
        label="Client ID"
        value={clientID}
        onChange={(e) => setClientID(e.target.value)}
        error={errors.clientID !== undefined}
        helperText={errors.clientID}
        required={enabled}
      />
      <PasswordField
        label="Client secret"
        value={clientSecret}
        onChange={(e) => setClientSecret(e.target.value)}
        error={errors.clientSecret !== undefined}
        helperText={
          errors.clientSecret ??
          (settings.client_secret_set
            ? "Leave empty to keep the current secret."
            : undefined)
        }
        autoComplete="new-password"
      />
      <TextField
        label="Scopes"
        value={scopes}
        onChange={(e) => setScopes(e.target.value)}
        error={errors.scopes !== undefined}
        helperText={errors.scopes ?? "Separated by spaces."}
      />
      <Stack direction="row" spacing={1} sx={{ alignItems: "start" }}>
        <TextField
          label="Session lifetime"
          type="number"
          value={lifetime}
          onChange={(e) => setLifetime(e.target.value)}
          error={errors.lifetime !== undefined}
          helperText={
            errors.lifetime ??
            "After this, users sign in through the identity provider again."
          }
          slotProps={{ htmlInput: { min: 1, step: 1 } }}
          sx={{ flex: 1 }}
        />
        <TextField
          select
          label="Unit"
          value={lifetimeUnit}
          onChange={(e) => setLifetimeUnit(Number(e.target.value))}
          sx={{ width: 130 }}
        >
          {unitsFor(settings.session_lifetime_seconds).map((u) => (
            <MenuItem key={u.seconds} value={u.seconds}>
              {u.label}
            </MenuItem>
          ))}
        </TextField>
      </Stack>
      <Tooltip
        describeChild
        title="Anyone who can set their email address at this provider can log in as the Ella IMS user with that address, Admins included. Turn this on only if the provider does not say whether addresses are verified, and controls its users' addresses."
      >
        <FormControlLabel
          control={
            <Switch
              checked={trustEmail}
              onChange={(e) => setTrustEmail(e.target.checked)}
            />
          }
          label="Trust email addresses from this provider"
        />
      </Tooltip>
    </EditDialog>
  );
}

export function OIDCGroupsDialog({
  settings,
  onClose,
}: {
  settings: OIDCSettings;
  onClose: () => void;
}) {
  const [groupsClaim, setGroupsClaim] = useState(settings.groups_claim);
  const nextID = useRef(settings.group_roles.length);
  const [rows, setRows] = useState<MappingRow[]>(() =>
    settings.group_roles.map((g, id) => ({ id, ...g })),
  );

  const trimmedGroups = rows.map((r) => r.group.trim());
  const mutation = useUpdateOIDC(onClose);

  const groups = groupErrors(trimmedGroups);
  const claimError = textError(
    groupsClaim.trim(),
    MAX_VALUE_BYTES,
    rows.length > 0 ? "Enter the claim that holds the groups." : undefined,
  );
  const valid =
    claimError === undefined &&
    groups.every((e) => e === undefined) &&
    rows.length <= MAX_GROUP_ROLES;

  const setRow = (i: number, change: Partial<MappingRow>) =>
    setRows((current) =>
      current.map((row, j) => (j === i ? { ...row, ...change } : row)),
    );

  return (
    <EditDialog
      title="Edit Roles from Groups"
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() =>
        mutation.mutate({
          ...currentUpdate(settings),
          groups_claim: groupsClaim.trim(),
          group_roles: rows.map((r, i) => ({
            group: trimmedGroups[i],
            role_id: r.role_id,
          })),
        })
      }
      onClose={onClose}
    >
      <Typography variant="body2" color="textSecondary">
        Optional. Users in a mapped group are created at their first sign-in. A
        user in several mapped groups gets the highest of their roles.
      </Typography>
      <TextField
        label="Groups claim"
        value={groupsClaim}
        onChange={(e) => setGroupsClaim(e.target.value)}
        error={claimError !== undefined}
        placeholder="groups"
        helperText={claimError ?? "The claim that holds the user's groups."}
        slotProps={{ inputLabel: { shrink: true } }}
      />
      {rows.map((row, i) => (
        <Stack
          key={row.id}
          direction={{ xs: "column", sm: "row" }}
          spacing={1}
          sx={{
            alignItems: { xs: "stretch", sm: "start" },
            borderWidth: { xs: 1, sm: 0 },
            borderStyle: "solid",
            borderColor: "divider",
            borderRadius: 1,
            p: { xs: 1.5, sm: 0 },
          }}
        >
          <TextField
            label={`Group ${i + 1}`}
            value={row.group}
            onChange={(e) => setRow(i, { group: e.target.value })}
            error={groups[i] !== undefined}
            helperText={groups[i]}
            required
            sx={{ flex: { sm: 1 } }}
          />
          <Box sx={{ flex: { sm: 1 } }}>
            <RoleField
              value={row.role_id}
              onChange={(role) => setRow(i, { role_id: role })}
              fullWidth
            />
          </Box>
          <Stack
            direction="row"
            sx={{ justifyContent: "flex-end", pt: { sm: 1 } }}
          >
            <Tooltip title="Remove">
              <IconButton
                aria-label={`Remove group ${i + 1}`}
                onClick={() =>
                  setRows((current) => current.filter((_, j) => j !== i))
                }
              >
                <DeleteIcon fontSize="small" />
              </IconButton>
            </Tooltip>
          </Stack>
        </Stack>
      ))}
      <Box>
        <Button
          startIcon={<AddIcon />}
          disabled={rows.length >= MAX_GROUP_ROLES}
          onClick={() => {
            const id = nextID.current++;
            setRows((current) => [
              ...current,
              { id, group: "", role_id: RoleID.ReadOnly },
            ]);
          }}
        >
          Add group
        </Button>
        {rows.length >= MAX_GROUP_ROLES && (
          <Typography
            variant="body2"
            color="textSecondary"
            component="span"
            sx={{ ml: 1 }}
          >
            At most {MAX_GROUP_ROLES} groups.
          </Typography>
        )}
      </Box>
    </EditDialog>
  );
}
