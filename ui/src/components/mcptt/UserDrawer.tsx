import type { ReactNode } from "react";
import {
  Box,
  Chip,
  Divider,
  Drawer,
  IconButton,
  Stack,
  Typography,
} from "@mui/material";
import { Close as CloseIcon } from "@mui/icons-material";
import CopyButton from "@/components/CopyButton";
import DrawerSection from "@/components/DrawerSection";
import Fields from "@/components/Fields";
import AffiliationChip from "@/components/mcptt/AffiliationChip";
import type {
  Affiliation,
  ClientRegistration,
  Group,
  MCPTTUser,
} from "@/queries/mcptt";
import { formatTimestamp } from "@/utils/dates";
import { groupLabel, groupsOf, sameIdentity, userLabel } from "@/utils/mcptt";

function UserDetail({
  user,
  groups,
  registrations,
  affiliations,
  domain,
  onClose,
}: {
  user: MCPTTUser;
  groups: Group[];
  registrations: ClientRegistration[];
  affiliations: Affiliation[];
  domain: string;
  onClose: () => void;
}) {
  const member = groupsOf(groups, user.mcptt_id);
  const phones = registrations.filter((r) =>
    sameIdentity(r.mcptt_id, user.mcptt_id),
  );
  const own = affiliations.filter((a) =>
    sameIdentity(a.mcptt_id, user.mcptt_id),
  );

  const rows: [string, ReactNode][] = [
    [
      "MCPTT ID",
      <Stack key="id" direction="row" sx={{ alignItems: "center", gap: 0.5 }}>
        <span>{user.mcptt_id}</span>
        <CopyButton value={user.mcptt_id} label="MCPTT ID" />
      </Stack>,
    ],
    [
      "IMS Identities",
      <Stack key="impus" component="span" spacing={0.5}>
        {user.impus.map((i) => (
          <span key={i}>{i}</span>
        ))}
      </Stack>,
    ],
  ];

  return (
    <Stack
      spacing={3}
      sx={{ p: 3, "& dl": { gridTemplateColumns: "8.5rem 1fr" } }}
    >
      <Stack direction="row" sx={{ alignItems: "flex-start", gap: 1 }}>
        <Typography
          id="user-drawer-title"
          variant="h5"
          component="h2"
          sx={{ flexGrow: 1, overflowWrap: "anywhere" }}
        >
          {userLabel(user, domain)}
        </Typography>
        <IconButton aria-label="Close" onClick={onClose}>
          <CloseIcon />
        </IconButton>
      </Stack>
      <Divider />
      <DrawerSection id="user-title" title="User">
        <Fields rows={rows} />
      </DrawerSection>
      <DrawerSection id="user-groups-title" title={`Groups (${member.length})`}>
        {member.length === 0 ? (
          <Typography variant="body2" color="text.secondary">
            A member of no group.
          </Typography>
        ) : (
          <Stack
            component="ul"
            spacing={1}
            sx={{ m: 0, p: 0, listStyle: "none" }}
          >
            {member.map((g) => {
              const always = user.implicit_affiliations.some((a) =>
                sameIdentity(a, g.group_id),
              );
              return (
                <Stack
                  key={g.group_id}
                  component="li"
                  direction="row"
                  sx={{ alignItems: "center", gap: 1, flexWrap: "wrap" }}
                >
                  <Box sx={{ flexGrow: 1 }}>{groupLabel(g, domain)}</Box>
                  {always && (
                    <Chip
                      size="small"
                      variant="outlined"
                      label="Always affiliated"
                    />
                  )}
                  <AffiliationChip
                    affiliations={own.filter((a) =>
                      sameIdentity(a.group_id, g.group_id),
                    )}
                  />
                </Stack>
              );
            })}
          </Stack>
        )}
      </DrawerSection>
      <DrawerSection id="user-phones-title" title={`Phones (${phones.length})`}>
        {phones.length === 0 ? (
          <Typography variant="body2" color="text.secondary">
            No phone is registered.
          </Typography>
        ) : (
          <Stack spacing={2}>
            {phones.map((p) => (
              <Fields
                key={p.client_id}
                rows={[
                  ["Client ID", p.client_id],
                  ["Registered At", p.impu],
                  ["Since", formatTimestamp(p.since)],
                  ["Expires", formatTimestamp(p.expires_at)],
                  ...(p.contacts.length > 0
                    ? ([["Contacts", p.contacts.join(", ")]] as [
                        string,
                        ReactNode,
                      ][])
                    : []),
                ]}
              />
            ))}
          </Stack>
        )}
      </DrawerSection>
    </Stack>
  );
}

export default function UserDrawer(props: {
  user: MCPTTUser | null;
  groups: Group[];
  registrations: ClientRegistration[];
  affiliations: Affiliation[];
  domain: string;
  onClose: () => void;
}) {
  const { user, onClose } = props;

  return (
    <Drawer
      anchor="right"
      open={user !== null}
      onClose={onClose}
      sx={{ zIndex: (theme) => theme.zIndex.modal }}
      slotProps={{
        paper: {
          sx: { width: { xs: "100%", sm: 520 } },
          "aria-labelledby": "user-drawer-title",
        },
      }}
    >
      {user && <UserDetail key={user.mcptt_id} {...props} user={user} />}
    </Drawer>
  );
}
