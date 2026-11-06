import { useState } from "react";
import type { ReactNode } from "react";
import {
  Box,
  Button,
  IconButton,
  FormControlLabel,
  Link,
  Skeleton,
  Switch,
  Stack,
  Tooltip,
  Typography,
} from "@mui/material";
import {
  ContentCopy as ContentCopyIcon,
  OpenInNew as OpenInNewIcon,
} from "@mui/icons-material";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import ConfirmDialog from "@/components/ConfirmDialog";
import { OIDCGroupsDialog, OIDCProviderDialog } from "@/components/OIDCDialog";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import SettingsTable from "@/components/SettingsTable";
import { useAuth } from "@/contexts/AuthContext";
import { errorMessage, useSnackbar } from "@/contexts/SnackbarContext";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import {
  currentUpdate,
  getOIDC,
  type OIDCSettings,
  updateOIDC,
} from "@/queries/oidc";
import { roleLabels } from "@/queries/users";
import { SSO_DOCS_URL } from "@/utils/docs";
import { describeDuration } from "@/utils/durations";

const orNone = (value: string) => value || "None";

const missingToEnable = (settings: OIDCSettings) =>
  (
    [
      [settings.external_url, "external URL"],
      [settings.issuer, "issuer URL"],
      [settings.client_id, "client ID"],
      [settings.client_secret_set, "client secret"],
      [settings.display_name, "display name"],
    ] as const
  )
    .filter(([value]) => !value)
    .map(([, label]) => label);

function SectionHeader({
  id,
  title,
  action,
}: {
  id: string;
  title: string;
  action: ReactNode;
}) {
  return (
    <Stack
      direction="row"
      sx={{
        mb: 2,
        alignItems: "center",
        justifyContent: "space-between",
        gap: 2,
      }}
    >
      <Typography id={id} variant="h6" component="h2">
        {title}
      </Typography>
      {action}
    </Stack>
  );
}

export default function SingleSignOn() {
  const auth = useAuth();
  const [editing, setEditing] = useState<
    "provider" | "groups" | "disable" | null
  >(null);
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const copy = useCopyToClipboard();
  const oidc = useQuery({ queryKey: ["oidc"], queryFn: getOIDC });
  const { data, isPending } = oidc;

  const toggle = useMutation({
    mutationFn: (enabled: boolean) =>
      updateOIDC({ ...currentUpdate(data!), enabled }),
    onSuccess: (updated) => {
      queryClient.setQueryData(["oidc"], updated);
      void queryClient.invalidateQueries({ queryKey: ["users"] });
      setEditing(null);
      showSnackbar(
        updated.enabled
          ? "Single sign-on enabled successfully."
          : "Single sign-on disabled successfully.",
        "success",
      );
    },
    onError: (error, enabled) => {
      setEditing(null);
      showSnackbar(
        `Failed to ${enabled ? "enable" : "disable"} single sign-on: ${errorMessage(error)}`,
        "error",
      );
    },
  });
  const missing = data ? missingToEnable(data) : [];

  const enabledSwitch = () => {
    if (!data) return <Skeleton variant="text" width={160} />;
    const canEdit = auth.can("oidc:update");
    const blocked = !data.enabled && missing.length > 0;
    return (
      <Tooltip
        describeChild
        title={canEdit && blocked ? `Missing: ${missing.join(", ")}.` : ""}
      >
        <FormControlLabel
          control={
            <Switch
              checked={data.enabled}
              disabled={!canEdit || blocked || toggle.isPending}
              onChange={(e) =>
                e.target.checked ? toggle.mutate(true) : setEditing("disable")
              }
            />
          }
          label="Enabled"
        />
      </Tooltip>
    );
  };

  const editButton = (section: "provider" | "groups", label: string) =>
    auth.can("oidc:update") && (
      <Button
        variant="contained"
        color="success"
        disabled={!data}
        aria-label={label}
        onClick={() => setEditing(section)}
      >
        Edit
      </Button>
    );

  return (
    <Box component="section" aria-labelledby="sso-title">
      <PageHeader
        id="sso-title"
        title="Single Sign-On"
        description={
          <>
            Let users sign in through your organization's identity provider,
            with OpenID Connect.{" "}
            <Link
              href={SSO_DOCS_URL}
              target="_blank"
              rel="noreferrer"
              sx={{
                color: "link",
                textDecoration: "underline",
                whiteSpace: "nowrap",
              }}
            >
              Configure Single Sign-On
              <OpenInNewIcon
                sx={{ fontSize: 14, ml: 0.5, verticalAlign: "-2px" }}
              />
            </Link>
          </>
        }
      />
      <QueryAlert query={oidc} subject="single sign-on settings" />
      <Box sx={{ mb: 3 }}>{enabledSwitch()}</Box>
      <Box component="section" aria-labelledby="sso-provider-title">
        <SectionHeader
          id="sso-provider-title"
          title="Provider"
          action={editButton("provider", "Edit provider")}
        />
        <SettingsTable
          label="Provider"
          loading={isPending}
          rows={[
            {
              label: "Display name",
              help: "The name on the login page's button.",
              value: data && orNone(data.display_name),
            },
            {
              label: "Issuer URL",
              help: "The identity provider.",
              value: data && orNone(data.issuer),
            },
            {
              label: "Client ID",
              help: "The client registered for Ella IMS at the identity provider.",
              value: data && orNone(data.client_id),
            },
            {
              label: "Client secret",
              help: "Never shown.",
              value: data && (data.client_secret_set ? "Set" : "Not set"),
            },
            {
              label: "Redirect URL",
              help: "Register this URL for the client at the identity provider.",
              value: data && (
                <Stack
                  direction="row"
                  spacing={1}
                  sx={{ alignItems: "center" }}
                >
                  <Typography
                    variant="body2"
                    sx={{ fontFamily: "monospace", overflowWrap: "anywhere" }}
                  >
                    {data.redirect_url || "Set the external URL first."}
                  </Typography>
                  {data.redirect_url && (
                    <Tooltip title="Copy">
                      <IconButton
                        size="small"
                        aria-label="Copy the redirect URL"
                        onClick={() =>
                          void copy(data.redirect_url, "Redirect URL")
                        }
                      >
                        <ContentCopyIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                  )}
                </Stack>
              ),
            },
            {
              label: "Scopes",
              help: "What Ella IMS asks the identity provider for.",
              value: data?.scopes.join(" "),
            },
            {
              label: "Session lifetime",
              help: "After this, users sign in through the identity provider again.",
              value: data && describeDuration(data.session_lifetime_seconds),
            },
            {
              label: "CA certificate",
              help: "Certificates trusted for the identity provider, instead of the system's.",
              value: data && (data.ca_certificate ? "Set" : "System"),
            },
            {
              label: "Trust email addresses",
              help: "Whether email addresses are trusted when the provider does not say they are verified.",
              value: data && (data.trust_unverified_email ? "Yes" : "No"),
            },
          ]}
        />
      </Box>
      <Box
        component="section"
        aria-labelledby="sso-groups-title"
        sx={{ mt: 4 }}
      >
        <SectionHeader
          id="sso-groups-title"
          title="Roles from groups"
          action={editButton("groups", "Edit roles from groups")}
        />
        <SettingsTable
          label="Roles from groups"
          loading={isPending}
          rows={[
            {
              label: "Groups claim",
              help: "The claim that holds the user's groups.",
              value: data && orNone(data.groups_claim),
            },
            {
              label: "Groups",
              help: "The first matching group gives the role.",
              value:
                data &&
                (data.group_roles.length === 0 ? (
                  "None"
                ) : (
                  <Stack spacing={0.5}>
                    {data.group_roles.map((g, i) => (
                      <Typography key={g.group} variant="body2">
                        {i + 1}. {g.group} → {roleLabels[g.role_id]}
                      </Typography>
                    ))}
                  </Stack>
                )),
            },
          ]}
        />
      </Box>
      {editing === "provider" && data && (
        <OIDCProviderDialog settings={data} onClose={() => setEditing(null)} />
      )}
      {editing === "groups" && data && (
        <OIDCGroupsDialog settings={data} onClose={() => setEditing(null)} />
      )}
      {editing === "disable" && (
        <ConfirmDialog
          title="Disable Single Sign-On"
          description="Disable single sign-on? Everyone who logged in with single sign-on is signed out."
          confirmLabel="Disable"
          pending={toggle.isPending}
          onConfirm={() => toggle.mutate(false)}
          onClose={() => setEditing(null)}
        />
      )}
    </Box>
  );
}
