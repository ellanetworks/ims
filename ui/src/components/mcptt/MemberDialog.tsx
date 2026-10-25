import { useState } from "react";
import {
  Autocomplete,
  Checkbox,
  FormControlLabel,
  FormGroup,
  TextField,
} from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import {
  type Group,
  type Member,
  mcpttKeys,
  type MCPTTUser,
  putMember,
} from "@/queries/mcptt";
import { sameIdentity, userLabel } from "@/utils/mcptt";

export default function MemberDialog({
  group,
  member,
  users,
  domain,
  onClose,
}: {
  group: Group;
  member?: Member;
  users: MCPTTUser[];
  domain: string;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [id, setID] = useState<string | null>(member?.mcptt_id ?? null);
  const [recvonly, setRecvonly] = useState(member?.recvonly ?? false);
  const [implicit, setImplicit] = useState(
    member?.implicit_affiliation ?? true,
  );

  const mutation = useMutation({
    mutationFn: (m: Member) => putMember(group.group_id, m),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.groups });
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.users });
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.affiliations });
      onClose();
    },
  });

  const candidates = users.filter(
    (u) => !group.members.some((m) => sameIdentity(m.mcptt_id, u.mcptt_id)),
  );
  const label = (mid: string) => {
    const u = users.find((x) => sameIdentity(x.mcptt_id, mid));
    return u ? userLabel(u, domain) : mid;
  };

  return (
    <EditDialog
      title={member ? `Edit ${label(member.mcptt_id)}` : "Add Member"}
      submitLabel={member ? "Update" : "Add"}
      pendingLabel={member ? "Updating…" : "Adding…"}
      valid={id !== null}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() =>
        mutation.mutate({
          mcptt_id: id ?? "",
          recvonly,
          implicit_affiliation: implicit,
        })
      }
      onClose={onClose}
    >
      {!member && (
        <Autocomplete
          options={candidates.map((u) => u.mcptt_id)}
          value={id}
          onChange={(_e, v) => setID(v)}
          getOptionLabel={label}
          renderInput={(params) => (
            <TextField
              {...params}
              label="User"
              required
              helperText={
                candidates.length === 0
                  ? "Every user is a member already."
                  : undefined
              }
            />
          )}
        />
      )}
      <FormGroup>
        <FormControlLabel
          control={
            <Checkbox
              checked={implicit}
              onChange={(e) => setImplicit(e.target.checked)}
            />
          }
          label="Always affiliated: the user's phones hear the group's calls"
        />
        <FormControlLabel
          control={
            <Checkbox
              checked={recvonly}
              onChange={(e) => setRecvonly(e.target.checked)}
            />
          }
          label="Listen only: may not talk"
        />
      </FormGroup>
    </EditDialog>
  );
}
