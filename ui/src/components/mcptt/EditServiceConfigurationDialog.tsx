import { useState } from "react";
import { Button, InputAdornment, TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import {
  mcpttKeys,
  type ServiceConfiguration,
  updateServiceConfiguration,
} from "@/queries/mcptt";

// DEFAULT_TRANSMIT_TIME_LIMIT_MS is T2 (Stop talking) when none is set (TS 24.380 Table 11.1.3-1).
export const DEFAULT_TRANSMIT_TIME_LIMIT_MS = 30000;

const DOMAIN =
  /^(?=.{1,253}$)([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)*[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.?$/;

function useUpdate(onClose: () => void) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: updateServiceConfiguration,
    onSuccess: (updated) => {
      queryClient.setQueryData(mcpttKeys.serviceConfiguration, updated);
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.users });
      onClose();
    },
  });
}

// EditDomainDialog edits the domain of the organisation: the IMS home domain unless set.
export function EditDomainDialog({
  config,
  homeDomain,
  onClose,
}: {
  config: ServiceConfiguration;
  homeDomain: string;
  onClose: () => void;
}) {
  const [domain, setDomain] = useState(config.domain);
  const mutation = useUpdate(onClose);
  const error = DOMAIN.test(domain.trim()) ? undefined : "A domain name";

  return (
    <EditDialog
      title="Edit Organisation Domain"
      valid={error === undefined}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate({ ...config, domain: domain.trim() })}
      onClose={onClose}
    >
      <TextField
        label="Organisation Domain"
        value={domain}
        onChange={(e) => setDomain(e.target.value)}
        error={error !== undefined}
        helperText={
          error ??
          "The domain of your MCPTT IDs and group IDs: a name such as alice typed elsewhere is of it. Existing IDs keep theirs."
        }
        slotProps={{
          input: {
            endAdornment: homeDomain && domain.trim() !== homeDomain && (
              <InputAdornment position="end">
                <Button size="small" onClick={() => setDomain(homeDomain)}>
                  Use home domain
                </Button>
              </InputAdornment>
            ),
          },
        }}
      />
    </EditDialog>
  );
}

// EditTalkTimeDialog edits the transmit time limit, T2: how long one may talk at a time.
export function EditTalkTimeDialog({
  config,
  onClose,
}: {
  config: ServiceConfiguration;
  onClose: () => void;
}) {
  const [seconds, setSeconds] = useState(
    String(
      (config.transmit_time_limit_ms ?? DEFAULT_TRANSMIT_TIME_LIMIT_MS) / 1000,
    ),
  );
  const mutation = useUpdate(onClose);
  const value = /^\d+(\.\d{1,3})?$/.test(seconds.trim())
    ? Math.round(Number(seconds) * 1000)
    : NaN;
  const error = value > 0 ? undefined : "A positive number of seconds";

  return (
    <EditDialog
      title="Edit Talk Time Limit"
      valid={error === undefined}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() =>
        mutation.mutate({ ...config, transmit_time_limit_ms: value })
      }
      onClose={onClose}
    >
      <TextField
        label="Talk Time Limit"
        value={seconds}
        onChange={(e) => setSeconds(e.target.value)}
        error={error !== undefined}
        helperText={
          error ??
          "How long one may talk at a time: the floor is then taken back. 30 s by default."
        }
        slotProps={{
          input: {
            endAdornment: <InputAdornment position="end">s</InputAdornment>,
          },
        }}
      />
    </EditDialog>
  );
}
