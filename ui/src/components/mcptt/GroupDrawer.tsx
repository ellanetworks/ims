import { useState, type ReactNode } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  Divider,
  Drawer,
  IconButton,
  Stack,
  Switch,
  Tooltip,
  Typography,
} from "@mui/material";
import {
  Add as AddIcon,
  Close as CloseIcon,
  Delete as DeleteIcon,
  Edit as EditIcon,
  Mic as MicIcon,
} from "@mui/icons-material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import ConfirmDialog from "@/components/ConfirmDialog";
import { useAuth } from "@/contexts/AuthContext";
import { errorMessage, useSnackbar } from "@/contexts/SnackbarContext";
import CopyButton from "@/components/CopyButton";
import DrawerSection from "@/components/DrawerSection";
import Fields from "@/components/Fields";
import AffiliationChip from "@/components/mcptt/AffiliationChip";
import MemberDialog from "@/components/mcptt/MemberDialog";
import {
  type Affiliation,
  deleteMember,
  type Group,
  type GroupCall,
  type Member,
  mcpttKeys,
  type MCPTTUser,
  putMember,
} from "@/queries/mcptt";
import {
  formatDuration,
  formatElapsed,
  groupLabel,
  sameIdentity,
  userLabel,
} from "@/utils/mcptt";

function MemberRow({
  group,
  member,
  name,
  affiliations,
  onEdit,
  onRemove,
}: {
  group: Group;
  member: Member;
  name: string;
  affiliations: Affiliation[];
  // onEdit and onRemove are absent for a role that cannot change the group, which sees the row read only.
  onEdit?: () => void;
  onRemove?: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const toggle = useMutation({
    mutationFn: (on: boolean) =>
      putMember(group.group_id, { ...member, implicit_affiliation: on }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.groups });
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.users });
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.affiliations });
      showSnackbar("Member updated successfully.", "success");
    },
    onError: (error) => {
      showSnackbar(`Failed to update member: ${errorMessage(error)}`, "error");
    },
  });
  const implicit = member.implicit_affiliation ?? false;

  return (
    <Box component="li" sx={{ py: 1, borderBottom: 1, borderColor: "divider" }}>
      <Stack
        direction="row"
        sx={{ alignItems: "center", gap: 1, flexWrap: "wrap" }}
      >
        <Box sx={{ flexGrow: 1, minWidth: 120 }}>{name}</Box>
        <AffiliationChip affiliations={affiliations} />
        <Tooltip
          title="Always affiliated: its phones hear the group's calls"
          arrow
        >
          <Switch
            size="small"
            checked={implicit}
            disabled={toggle.isPending || !onEdit}
            onChange={(e) => toggle.mutate(e.target.checked)}
            slotProps={{ input: { "aria-label": `${name} always affiliated` } }}
          />
        </Tooltip>
        {onEdit && (
          <IconButton size="small" aria-label={`Edit ${name}`} onClick={onEdit}>
            <EditIcon fontSize="small" color="primary" />
          </IconButton>
        )}
        {onRemove && (
          <IconButton
            size="small"
            aria-label={`Remove ${name}`}
            onClick={onRemove}
          >
            <DeleteIcon fontSize="small" color="primary" />
          </IconButton>
        )}
      </Stack>
      {member.recvonly && (
        <Chip
          size="small"
          variant="outlined"
          label="Listen only"
          sx={{ mt: 0.5 }}
        />
      )}
      {toggle.error && (
        <Alert severity="error" sx={{ mt: 1 }}>
          Could not update: {toggle.error.message}
        </Alert>
      )}
    </Box>
  );
}

function GroupDetail({
  group,
  users,
  affiliations,
  call,
  domain,
  onClose,
}: {
  group: Group;
  users: MCPTTUser[];
  affiliations: Affiliation[];
  call?: GroupCall;
  domain: string;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const editable = useAuth().can("mcptt:update");
  const [memberDialog, setMemberDialog] = useState<{ member?: Member } | null>(
    null,
  );
  const [removing, setRemoving] = useState<Member | null>(null);
  const removal = useMutation({
    mutationFn: (m: Member) => deleteMember(group.group_id, m.mcptt_id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.groups });
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.users });
      setRemoving(null);
      showSnackbar("Member removed successfully.", "success");
    },
    onError: (error) => {
      setRemoving(null);
      showSnackbar(`Failed to remove member: ${errorMessage(error)}`, "error");
    },
  });

  const nameOf = (id: string) => {
    const u = users.find((x) => sameIdentity(x.mcptt_id, id));
    return u ? userLabel(u, domain) : id;
  };
  const own = affiliations.filter((a) =>
    sameIdentity(a.group_id, group.group_id),
  );

  const rows: [string, ReactNode][] = [
    [
      "Group ID",
      <Stack key="id" direction="row" sx={{ alignItems: "center", gap: 0.5 }}>
        <span>{group.group_id}</span>
        <CopyButton value={group.group_id} label="Group ID" />
      </Stack>,
    ],
    ["Maximum Duration", formatDuration(group.maximum_duration_ms)],
    [
      "Hang Time",
      group.hang_timer_ms === undefined
        ? "30 s (default)"
        : formatDuration(group.hang_timer_ms),
    ],
    [
      "Initiator Leaves",
      group.release_on_initiator_leave ? "The call ends" : "The call goes on",
    ],
  ];

  return (
    <Stack
      spacing={3}
      sx={{ p: 3, "& dl": { gridTemplateColumns: "10.5rem 1fr" } }}
    >
      <Stack direction="row" sx={{ alignItems: "flex-start", gap: 1 }}>
        <Typography
          id="group-drawer-title"
          variant="h5"
          component="h2"
          sx={{ flexGrow: 1, overflowWrap: "anywhere" }}
        >
          {groupLabel(group, domain)}
        </Typography>
        <IconButton aria-label="Close" onClick={onClose}>
          <CloseIcon />
        </IconButton>
      </Stack>
      <Divider />
      {call && (
        <Alert severity="success" icon={<MicIcon />}>
          {call.talker
            ? `${nameOf(call.talker.mcptt_id)} is talking (${formatElapsed(call.talker.since)}).`
            : "A call is in progress; nobody is talking."}{" "}
          {call.participants.filter((p) => p.state === "joined").length} joined.
        </Alert>
      )}
      <DrawerSection id="group-title" title="Group">
        <Fields rows={rows} />
      </DrawerSection>
      <Box component="section" aria-labelledby="group-members-title">
        <Stack
          direction="row"
          sx={{ alignItems: "center", justifyContent: "space-between", mb: 1 }}
        >
          <Typography
            id="group-members-title"
            variant="subtitle1"
            component="h3"
          >
            Members ({group.members.length})
          </Typography>
          {editable && (
            <Button
              size="small"
              startIcon={<AddIcon />}
              onClick={() => setMemberDialog({})}
            >
              Add Member
            </Button>
          )}
        </Stack>
        {group.members.length === 0 ? (
          <Typography variant="body2" color="text.secondary">
            No members yet.
          </Typography>
        ) : (
          <Box component="ul" sx={{ m: 0, p: 0, listStyle: "none" }}>
            {group.members.map((m) => (
              <MemberRow
                key={m.mcptt_id}
                group={group}
                member={m}
                name={nameOf(m.mcptt_id)}
                affiliations={own.filter((a) =>
                  sameIdentity(a.mcptt_id, m.mcptt_id),
                )}
                onEdit={
                  editable ? () => setMemberDialog({ member: m }) : undefined
                }
                onRemove={
                  editable
                    ? () => {
                        removal.reset();
                        setRemoving(m);
                      }
                    : undefined
                }
              />
            ))}
          </Box>
        )}
      </Box>
      {memberDialog && (
        <MemberDialog
          group={group}
          member={memberDialog.member}
          users={users}
          domain={domain}
          onClose={() => setMemberDialog(null)}
        />
      )}
      {removing && (
        <ConfirmDialog
          title="Remove Member"
          description={`Remove ${nameOf(removing.mcptt_id)} from ${groupLabel(group, domain)}? The user leaves the group's call in progress, if any, and can no longer call the group nor hear its calls.`}
          confirmLabel="Remove"
          pending={removal.isPending}
          onConfirm={() => removal.mutate(removing)}
          onClose={() => setRemoving(null)}
        />
      )}
    </Stack>
  );
}

export default function GroupDrawer(props: {
  group: Group | null;
  users: MCPTTUser[];
  affiliations: Affiliation[];
  call?: GroupCall;
  domain: string;
  onClose: () => void;
}) {
  const { group, onClose } = props;

  return (
    <Drawer
      anchor="right"
      open={group !== null}
      onClose={onClose}
      sx={{ zIndex: (theme) => theme.zIndex.modal }}
      slotProps={{
        paper: {
          sx: { width: { xs: "100%", sm: 560 } },
          "aria-labelledby": "group-drawer-title",
        },
      }}
    >
      {group && <GroupDetail key={group.group_id} {...props} group={group} />}
    </Drawer>
  );
}
