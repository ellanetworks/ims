import { useState } from "react";
import {
  FormLabel,
  IconButton,
  List,
  ListItem,
  ListItemText,
  MenuItem,
  Stack,
  TextField,
} from "@mui/material";
import {
  ArrowDownward as ArrowDownwardIcon,
  ArrowUpward as ArrowUpwardIcon,
  Delete as DeleteIcon,
} from "@mui/icons-material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { useSnackbar } from "@/contexts/SnackbarContext";
import {
  createSIPRoute,
  type SIPRoute,
  type SIPRouteParams,
  type SIPTrunk,
  updateSIPRoute,
} from "@/queries/sipTrunks";
import { TABLE_CONTAINER_SX } from "@/utils/layout";

const PREFIX = /^(\+[1-9]\d{0,14})?$/;

const move = (ids: string[], from: number, to: number): string[] => {
  const next = [...ids];
  [next[from], next[to]] = [next[to], next[from]];
  return next;
};

export default function SIPRouteDialog({
  route,
  trunks,
  onClose,
}: {
  route?: SIPRoute;
  trunks: SIPTrunk[];
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [prefix, setPrefix] = useState(route?.prefix ?? "");
  const [selected, setSelected] = useState<string[]>(route?.trunks ?? []);

  const mutation = useMutation({
    mutationFn: (params: SIPRouteParams) =>
      route ? updateSIPRoute(route.id, params) : createSIPRoute(params),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["sip-routes"] });
      showSnackbar(
        route ? "Route updated successfully." : "Route created successfully.",
        "success",
      );
      onClose();
    },
  });

  const nameOf = (id: string) => trunks.find((t) => t.id === id)?.name ?? id;
  const available = trunks.filter((t) => !selected.includes(t.id));

  const prefixError = PREFIX.test(prefix)
    ? undefined
    : "+ and 1 to 15 digits, not starting with 0";
  const valid = prefixError === undefined && selected.length > 0;

  return (
    <EditDialog
      title={route ? "Edit Route" : "Create Route"}
      submitLabel={route ? "Update" : "Create"}
      pendingLabel={route ? "Updating…" : "Creating…"}
      failure={
        route ? "Could not update the route" : "Could not create the route"
      }
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate({ prefix, trunks: selected })}
      onClose={onClose}
    >
      <TextField
        label="Prefix"
        value={prefix}
        onChange={(e) => setPrefix(e.target.value.trim())}
        placeholder="Default"
        error={prefixError !== undefined}
        helperText={prefixError}
        slotProps={{ inputLabel: { shrink: true } }}
      />
      <Stack spacing={1}>
        <FormLabel id="route-trunks-label" required>
          Trunks
        </FormLabel>
        {selected.length > 0 && (
          <List
            dense
            aria-labelledby="route-trunks-label"
            sx={{ ...TABLE_CONTAINER_SX, py: 0 }}
          >
            {selected.map((id, index) => (
              <ListItem
                key={id}
                divider={index < selected.length - 1}
                secondaryAction={
                  <>
                    <IconButton
                      size="small"
                      aria-label={`Move ${nameOf(id)} up`}
                      disabled={index === 0}
                      onClick={() =>
                        setSelected((ids) => move(ids, index, index - 1))
                      }
                    >
                      <ArrowUpwardIcon fontSize="small" />
                    </IconButton>
                    <IconButton
                      size="small"
                      aria-label={`Move ${nameOf(id)} down`}
                      disabled={index === selected.length - 1}
                      onClick={() =>
                        setSelected((ids) => move(ids, index, index + 1))
                      }
                    >
                      <ArrowDownwardIcon fontSize="small" />
                    </IconButton>
                    <IconButton
                      size="small"
                      aria-label={`Remove ${nameOf(id)}`}
                      onClick={() =>
                        setSelected((ids) => ids.filter((i) => i !== id))
                      }
                    >
                      <DeleteIcon fontSize="small" />
                    </IconButton>
                  </>
                }
              >
                <ListItemText primary={`${index + 1}. ${nameOf(id)}`} />
              </ListItem>
            ))}
          </List>
        )}
        <TextField
          select
          label="Add Trunk"
          value=""
          onChange={(e) => setSelected((ids) => [...ids, e.target.value])}
          disabled={available.length === 0}
        >
          {available.map((t) => (
            <MenuItem key={t.id} value={t.id}>
              {t.name}
            </MenuItem>
          ))}
        </TextField>
      </Stack>
    </EditDialog>
  );
}
