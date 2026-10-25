import { useState } from "react";
import { Alert, Box, Chip, Stack, Typography } from "@mui/material";
import CopyButton from "@/components/CopyButton";
import DomainName from "@/components/DomainName";
import QueryAlert from "@/components/QueryAlert";
import SettingsTable from "@/components/SettingsTable";
import {
  DEFAULT_TRANSMIT_TIME_LIMIT_MS,
  EditDomainDialog,
  EditTalkTimeDialog,
} from "@/components/mcptt/EditServiceConfigurationDialog";
import { useMCPTTSettings } from "@/hooks/useMCPTT";
import type { MCPTTStatus } from "@/queries/status";
import { formatDuration } from "@/utils/mcptt";

function Copyable({ value, label }: { value: string; label: string }) {
  return (
    <Stack direction="row" sx={{ alignItems: "center", gap: 0.5 }}>
      <span style={{ overflowWrap: "anywhere" }}>
        <DomainName name={value} />
      </span>
      <CopyButton value={value} label={label} />
    </Stack>
  );
}

function Section({
  id,
  title,
  description,
  first,
}: {
  id: string;
  title: string;
  description: string;
  first?: boolean;
}) {
  return (
    <Box sx={{ mt: first ? 0 : 4, mb: 2 }}>
      <Typography id={id} variant="h6" component="h2">
        {title}
      </Typography>
      <Typography variant="body2" color="text.secondary">
        {description}
      </Typography>
    </Box>
  );
}

export function SettingsTab({ status }: { status: MCPTTStatus }) {
  const [editing, setEditing] = useState<"domain" | "talk-time" | null>(null);
  const { config, homeDomain } = useMCPTTSettings();
  const c = config.data;

  return (
    <>
      <Section
        id="server-title"
        title="This MCPTT Server"
        description="In your HSS, add an application server of this name, and the MCPTT filter criteria to your subscribers' profiles. In Ella Core, use the MCPTT preset."
        first
      />
      {status.mode === "direct" && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          Direct mode: the MCPTT server is its phones' registrar, without the
          IMS. For the lab only.
        </Alert>
      )}
      <SettingsTable
        label="This MCPTT server"
        loading={false}
        rows={[
          {
            label: "Server Name",
            help: "The application server's name in your HSS and its filter criteria.",
            value: <Copyable value={status.name} label="Server Name" />,
          },
          {
            label: "Participating PSI",
            help: "Where phones send their MCPTT requests. Phones default to it.",
            value: (
              <Copyable
                value={status.participating_psi}
                label="Participating PSI"
              />
            ),
          },
        ]}
      />

      <Section
        id="service-title"
        title="Service"
        description="Your organisation's push-to-talk service, for all its users and groups."
      />
      <QueryAlert
        error={config.error}
        hasData={c !== undefined}
        subject="service configuration"
      />
      <SettingsTable
        label="Service"
        loading={config.isPending}
        rows={[
          {
            label: "Organisation Domain",
            help: "The domain of your MCPTT IDs and group IDs.",
            value: c && (
              <Stack direction="row" sx={{ alignItems: "center", gap: 1 }}>
                <span style={{ overflowWrap: "anywhere" }}>
                  <DomainName name={c.domain} />
                </span>
                {c.domain === homeDomain && (
                  <Chip label="home domain" size="small" variant="outlined" />
                )}
              </Stack>
            ),
            onEdit: () => setEditing("domain"),
          },
          {
            label: "Talk Time Limit",
            help: "How long one may talk at a time.",
            value:
              c &&
              (c.transmit_time_limit_ms === undefined
                ? `${formatDuration(DEFAULT_TRANSMIT_TIME_LIMIT_MS)} (default)`
                : formatDuration(c.transmit_time_limit_ms)),
            onEdit: () => setEditing("talk-time"),
          },
        ]}
      />
      {editing === "domain" && c && (
        <EditDomainDialog
          config={c}
          homeDomain={homeDomain}
          onClose={() => setEditing(null)}
        />
      )}
      {editing === "talk-time" && c && (
        <EditTalkTimeDialog config={c} onClose={() => setEditing(null)} />
      )}
    </>
  );
}
