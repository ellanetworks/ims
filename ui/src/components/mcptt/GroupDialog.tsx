import { useState } from "react";
import {
  Autocomplete,
  Checkbox,
  FormControlLabel,
  InputAdornment,
  TextField,
} from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { aliasError } from "@/components/mcptt/UserDialog";
import {
  createGroup,
  type Group,
  mcpttKeys,
  type MCPTTUser,
  updateGroup,
} from "@/queries/mcptt";
import { expandID, sameIdentity, userLabel } from "@/utils/mcptt";
import { useSnackbar } from "@/contexts/SnackbarContext";

// DEFAULT_DURATION_MIN is the maximum duration of a new group's calls.
const DEFAULT_DURATION_MIN = 10;

// DEFAULT_HANG_TIME_S is T4 (Inactivity) when none is set (TS 24.380 Table 11.1.3-1).
export const DEFAULT_HANG_TIME_S = 30;

export default function GroupDialog({
  group,
  users,
  domain,
  onClose,
}: {
  group?: Group;
  users: MCPTTUser[];
  domain: string;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [name, setName] = useState(group?.display_name ?? "");
  const [id, setID] = useState(group?.group_id ?? "");
  const [duration, setDuration] = useState(
    String(group ? group.maximum_duration_ms / 60000 : DEFAULT_DURATION_MIN),
  );
  // An empty hang time is the server's default, which a group without one keeps following.
  const [hang, setHang] = useState(
    group?.hang_timer_ms === undefined
      ? ""
      : String(group.hang_timer_ms / 1000),
  );
  const [members, setMembers] = useState<string[]>([]);
  const [affiliate, setAffiliate] = useState(true);
  const [releaseOnLeave, setReleaseOnLeave] = useState(
    group?.release_on_initiator_leave ?? false,
  );

  const mutation = useMutation({
    mutationFn: (g: Group) => (group ? updateGroup(g) : createGroup(g)),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.groups });
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.users });
      showSnackbar(
        group ? "Group updated successfully." : "Group created successfully.",
        "success",
      );
      onClose();
    },
  });

  const expanded = group ? group.group_id : expandID(id, domain);
  const minutes = /^\d+(\.\d+)?$/.test(duration.trim())
    ? Number(duration)
    : NaN;
  const defaultHang = hang.trim() === "";
  const seconds = /^\d+(\.\d{1,3})?$/.test(hang.trim()) ? Number(hang) : NaN;

  const errors = {
    name: aliasError(name, "name"),
    id: expanded
      ? undefined
      : id.trim() === ""
        ? "Required"
        : "A name, group@domain or a SIP URI",
    duration: minutes > 0 ? undefined : "A positive number of minutes",
    hang: defaultHang || seconds >= 0 ? undefined : "A number of seconds",
  };
  const valid = Object.values(errors).every((e) => e === undefined);

  const submit = () =>
    mutation.mutate({
      group_id: expanded ?? "",
      display_name: name,
      maximum_duration_ms: Math.round(minutes * 60000),
      hang_timer_ms: defaultHang ? undefined : Math.round(seconds * 1000),
      release_on_initiator_leave: releaseOnLeave,
      // An update keeps the members as they are, and their affiliations.
      members: group
        ? group.members.map((m) => ({
            mcptt_id: m.mcptt_id,
            recvonly: m.recvonly,
          }))
        : members.map((mcptt_id) => ({
            mcptt_id,
            recvonly: false,
            implicit_affiliation: affiliate,
          })),
    });

  return (
    <EditDialog
      title={group ? "Edit Group" : "Create Group"}
      submitLabel={group ? "Update" : "Create"}
      pendingLabel={group ? "Updating…" : "Creating…"}
      failure={
        group ? "Could not update the group" : "Could not create the group"
      }
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={submit}
      onClose={onClose}
    >
      <TextField
        label="Name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        error={errors.name !== undefined}
        helperText={
          errors.name ?? "Optional. How people know the group, e.g. Fire."
        }
      />
      <TextField
        label="Group ID"
        value={group ? group.group_id : id}
        onChange={(e) => setID(e.target.value)}
        disabled={group !== undefined}
        required
        error={!group && id !== "" && errors.id !== undefined}
        helperText={
          group
            ? "A group keeps its ID."
            : (expanded ??
              `A name such as fire, of ${domain}, or a SIP URI. Phones call the group by this ID.`)
        }
      />
      {!group && (
        <>
          <Autocomplete
            multiple
            options={users.map((u) => u.mcptt_id)}
            value={members}
            onChange={(_e, v) => setMembers(v)}
            getOptionLabel={(mid) => {
              const u = users.find((x) => sameIdentity(x.mcptt_id, mid));
              return u ? userLabel(u, domain) : mid;
            }}
            renderInput={(params) => (
              <TextField
                {...params}
                label="Members"
                helperText="Optional. You can add members later."
              />
            )}
          />
          <FormControlLabel
            control={
              <Checkbox
                checked={affiliate}
                onChange={(e) => setAffiliate(e.target.checked)}
              />
            }
            label="Members are always affiliated: their phones hear the group's calls"
          />
        </>
      )}
      <TextField
        label="Maximum Call Duration"
        value={duration}
        onChange={(e) => setDuration(e.target.value)}
        error={errors.duration !== undefined}
        helperText={errors.duration ?? "A call ends after this long."}
        slotProps={{
          input: {
            endAdornment: <InputAdornment position="end">min</InputAdornment>,
          },
        }}
      />
      <TextField
        label="Hang Time"
        value={hang}
        onChange={(e) => setHang(e.target.value)}
        error={errors.hang !== undefined}
        placeholder={String(DEFAULT_HANG_TIME_S)}
        helperText={
          errors.hang ??
          `A call ends after this long without anyone talking; ${DEFAULT_HANG_TIME_S} s if empty.`
        }
        slotProps={{
          input: {
            endAdornment: <InputAdornment position="end">s</InputAdornment>,
          },
        }}
      />
      <FormControlLabel
        control={
          <Checkbox
            checked={releaseOnLeave}
            onChange={(e) => setReleaseOnLeave(e.target.checked)}
          />
        }
        label="End the call when the member who started it leaves"
      />
    </EditDialog>
  );
}
