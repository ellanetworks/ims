import { useState } from "react";
import { Autocomplete, Chip, TextField } from "@mui/material";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import {
  createUser,
  type Group,
  mcpttKeys,
  type MCPTTUser,
  updateUser,
} from "@/queries/mcptt";
import { listRegistrations } from "@/queries/registrations";
import {
  expandID,
  expandIMPU,
  isMCPTTIdentity,
  groupLabel,
  groupsOf,
  sameIdentity,
} from "@/utils/mcptt";
import { identitiesOf } from "@/utils/registrations";
import { useSnackbar } from "@/contexts/SnackbarContext";

const MAX_ALIAS = 64;

// aliasError is the problem with an alias, as the server validates it.
export const aliasError = (alias: string, what: string): string | undefined => {
  if ([...alias].length > MAX_ALIAS) return `At most ${MAX_ALIAS} characters`;
  if (alias.trim() !== alias) return `The ${what} begins or ends with a space`;
  // oxlint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f-\u009f]/.test(alias))
    return "No control characters";
  return undefined;
};

export default function UserDialog({
  user,
  groups,
  domain,
  homeDomain,
  onClose,
}: {
  user?: MCPTTUser;
  groups: Group[];
  domain: string;
  homeDomain: string;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [alias, setAlias] = useState(user?.alias ?? "");
  const [id, setID] = useState(user?.mcptt_id ?? "");
  const [impus, setIMPUs] = useState<string[]>(user?.impus ?? []);
  const [impuInput, setIMPUInput] = useState("");
  const [implicit, setImplicit] = useState<string[]>(
    user?.implicit_affiliations ?? [],
  );

  // The identities the IMS registered, which the HSS wrote as the server compares them.
  const registered = useQuery({
    queryKey: ["registrations", "", { page: 0, pageSize: 100 }],
    queryFn: () => listRegistrations({ page: 1, perPage: 100 }),
  });
  const options = [
    ...new Set(
      (registered.data?.items ?? []).flatMap((r) =>
        identitiesOf(r)
          .filter((i) => !i.barred && isMCPTTIdentity(i.uri))
          .map((i) => i.uri),
      ),
    ),
  ];

  const mutation = useMutation({
    mutationFn: (u: MCPTTUser) => (user ? updateUser(u) : createUser(u)),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.users });
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.groups });
      showSnackbar(
        user ? "User updated successfully." : "User created successfully.",
        "success",
      );
      onClose();
    },
  });

  const expanded = user ? user.mcptt_id : expandID(id, domain);
  const member = user ? groupsOf(groups, user.mcptt_id) : [];
  const pendingIMPU =
    impuInput.trim() === "" ? undefined : expandIMPU(impuInput, homeDomain);
  const all = pendingIMPU ? [...impus, pendingIMPU] : impus;

  const errors = {
    alias: aliasError(alias, "name"),
    id: expanded
      ? undefined
      : id.trim() === ""
        ? "Required"
        : "A name, user@domain or a SIP URI",
    impus:
      impuInput.trim() !== "" && !pendingIMPU
        ? "A number, a name, user@domain or a SIP or tel URI"
        : all.length === 0
          ? "At least one"
          : undefined,
  };
  const valid = Object.values(errors).every((e) => e === undefined);

  const addIMPU = (value: string) => {
    const uri = expandIMPU(value, homeDomain);
    if (uri && !impus.some((i) => sameIdentity(i, uri)))
      setIMPUs([...impus, uri]);
    setIMPUInput("");
  };

  return (
    <EditDialog
      title={user ? "Edit User" : "Create User"}
      submitLabel={user ? "Update" : "Create"}
      pendingLabel={user ? "Updating…" : "Creating…"}
      failure={user ? "Could not update the user" : "Could not create the user"}
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() =>
        mutation.mutate({
          mcptt_id: expanded ?? "",
          alias,
          impus: all,
          implicit_affiliations: implicit,
        })
      }
      onClose={onClose}
    >
      <TextField
        label="Name"
        value={alias}
        onChange={(e) => setAlias(e.target.value)}
        error={errors.alias !== undefined}
        helperText={
          errors.alias ?? "Optional. How people know the user, e.g. Alice."
        }
      />
      <TextField
        label="MCPTT ID"
        value={user ? user.mcptt_id : id}
        onChange={(e) => setID(e.target.value)}
        disabled={user !== undefined}
        required
        error={!user && id !== "" && errors.id !== undefined}
        helperText={
          user
            ? "A user keeps its MCPTT ID."
            : !expanded
              ? `A name such as alice, of ${domain}, or a SIP URI.`
              : expanded
        }
      />
      <Autocomplete
        multiple
        freeSolo
        options={options}
        value={impus}
        inputValue={impuInput}
        onInputChange={(_e, v, reason) => {
          if (reason !== "reset") setIMPUInput(v);
        }}
        onChange={(_e, values, reason) => {
          if (reason === "createOption" || reason === "selectOption") {
            addIMPU(String(values[values.length - 1]));
          } else {
            setIMPUs(values.map(String));
          }
        }}
        renderValue={(values, getItemProps) =>
          values.map((v, index) => {
            const { key, ...props } = getItemProps({ index });
            return <Chip key={key} label={v} size="small" {...props} />;
          })
        }
        renderInput={(params) => (
          <TextField
            {...params}
            label="IMS Identities"
            required
            error={
              errors.impus !== undefined &&
              (impus.length > 0 || impuInput !== "")
            }
            helperText={
              errors.impus && (impus.length > 0 || impuInput !== "")
                ? errors.impus
                : pendingIMPU
                  ? `Adds ${pendingIMPU}`
                  : `The MCPTT identities of the phones, as your HSS has them: pick a registered one, or type an MSISDN for sip:mcptt-<MSISDN>@${homeDomain}.`
            }
          />
        )}
      />
      {user && (
        <Autocomplete
          multiple
          options={member.map((g) => g.group_id)}
          value={implicit}
          onChange={(_e, values) => setImplicit(values)}
          getOptionLabel={(gid) => {
            const g = member.find((m) => sameIdentity(m.group_id, gid));
            return g ? groupLabel(g, domain) : gid;
          }}
          isOptionEqualToValue={(a, b) => sameIdentity(a, b)}
          renderInput={(params) => (
            <TextField
              {...params}
              label="Always Affiliated To"
              helperText={
                member.length === 0
                  ? "The user is a member of no group yet."
                  : "Groups the user's phones affiliate to at their next registration, and hear the calls of."
              }
            />
          )}
        />
      )}
    </EditDialog>
  );
}
