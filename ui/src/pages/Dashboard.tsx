import type { ReactNode } from "react";
import {
  Box,
  Card,
  type CardProps,
  CardActionArea,
  CardContent,
  CardHeader,
  Chip,
  Skeleton,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from "@mui/material";
import Grid from "@mui/material/Grid";
import { useQuery } from "@tanstack/react-query";
import { Link as RouterLink, useNavigate } from "react-router-dom";
import { OutcomeChip } from "@/components/CallOutcomeChip";
import DomainName from "@/components/DomainName";
import PageHeader from "@/components/PageHeader";
import PeerStateChip from "@/components/PeerStateChip";
import { useAuth } from "@/contexts/AuthContext";
import { useMCPTTStatus } from "@/hooks/useMCPTT";
import {
  CALL_OUTCOMES,
  countCallRecords,
  type CallOutcome,
} from "@/queries/callRecords";
import { listDiameterPeers, peerName } from "@/queries/diameter";
import { getIPSMGW } from "@/queries/ipsmgw";
import {
  countClientRegistrations,
  countGroupCalls,
  mcpttKeys,
} from "@/queries/mcptt";
import { getMetrics, sumOf } from "@/queries/metrics";
import { getOperator } from "@/queries/operator";
import { getSIPStatus } from "@/queries/sip";
import { listSIPTrunks } from "@/queries/sipTrunks";
import { pcscfAddresses } from "@/utils/addresses";
import { formatDateTime } from "@/utils/dates";

export const DASHBOARD_REFRESH_MS = 5000;

export const COUNTS_REFRESH_MS = 30_000;

const DAY_MS = 24 * 60 * 60 * 1000;

const nf = new Intl.NumberFormat();

const BYTE_UNITS = ["B", "KiB", "MiB", "GiB", "TiB"];

const sizeFormat = new Intl.NumberFormat(undefined, {
  maximumFractionDigits: 2,
});

export const formatBytes = (bytes: number): string => {
  let n = bytes;
  let i = 0;
  while (n >= 1024 && i < BYTE_UNITS.length - 1) {
    n /= 1024;
    i++;
  }
  return `${sizeFormat.format(n)} ${BYTE_UNITS[i]}`;
};

const ALWAYS_SHOWN: CallOutcome[] = ["answered", "failed"];

const lastDayLink = (outcome?: CallOutcome) =>
  `/calls?${outcome ? `outcome=${outcome}&` : ""}range=24h`;

const headerSx = {
  backgroundColor: "backgroundSubtle",
  borderTopLeftRadius: 8,
  borderTopRightRadius: 8,
  "& .MuiCardHeader-title": { fontWeight: 600, fontSize: "1rem" },
  "& .MuiCardHeader-action": { alignSelf: "center", m: 0 },
};

const titleLinkSx = {
  textDecoration: "none",
  color: "inherit",
  "&:hover": { textDecoration: "underline" },
};

const headSx = { fontWeight: 600, whiteSpace: "nowrap" };

interface PanelProps extends Omit<CardProps, "title"> {
  title: string;
  to?: string;
  action?: ReactNode;
  loading?: boolean;
  error?: boolean;
  minHeight?: number;
  alignTop?: boolean;
  children?: ReactNode;
}

function Panel({
  title,
  to,
  action,
  loading,
  error,
  minHeight = 160,
  alignTop,
  children,
  ...cardProps
}: PanelProps) {
  return (
    <Card
      {...cardProps}
      sx={{
        height: "100%",
        display: "flex",
        flexDirection: "column",
        borderRadius: 3,
        boxShadow: 2,
      }}
    >
      <CardHeader
        title={
          to ? (
            <Box component={RouterLink} to={to} sx={titleLinkSx}>
              {title}
            </Box>
          ) : (
            title
          )
        }
        action={loading || error ? undefined : action}
        slotProps={{ title: { component: "h3" } }}
        sx={headerSx}
      />
      <CardContent
        sx={{
          flexGrow: 1,
          display: "flex",
          alignItems: alignTop ? "flex-start" : "center",
          justifyContent: alignTop ? "flex-start" : "center",
          minHeight,
        }}
      >
        {loading ? (
          <Skeleton
            variant="rounded"
            width={alignTop ? "100%" : 120}
            height={alignTop ? minHeight - 32 : 40}
          />
        ) : error ? (
          <Typography variant="body2" color="error">
            Failed to load data.
          </Typography>
        ) : (
          children
        )}
      </CardContent>
    </Card>
  );
}

interface KpiCardProps {
  title: string;
  hint?: string;
  value?: string;
  alert?: boolean;
  loading?: boolean;
  error?: boolean;
  to?: string;
}

function KpiCard({ title, hint, value, alert, to, ...state }: KpiCardProps) {
  const content = (
    <Typography variant="h4" color={alert ? "error" : undefined}>
      {value}
    </Typography>
  );

  const card = to ? (
    <CardActionArea
      component={RouterLink}
      to={to}
      sx={{ height: "100%", borderRadius: 3 }}
    >
      <Panel title={title} {...state}>
        {content}
      </Panel>
    </CardActionArea>
  ) : (
    <Panel title={title} tabIndex={0} {...state}>
      {content}
    </Panel>
  );

  return hint ? (
    <Tooltip title={hint} arrow describeChild>
      {card}
    </Tooltip>
  ) : (
    card
  );
}

function Section({
  title,
  first,
  children,
}: {
  title: string;
  first?: boolean;
  children: ReactNode;
}) {
  return (
    <Box component="section" aria-label={title}>
      <Typography variant="h5" component="h2" sx={{ mt: first ? 0 : 4, mb: 2 }}>
        {title}
      </Typography>
      <Grid container spacing={4} sx={{ alignItems: "stretch" }}>
        {children}
      </Grid>
    </Box>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Box>
      <Typography variant="body2" color="textSecondary">
        {label}
      </Typography>
      <Typography
        variant="body1"
        component="div"
        sx={{ overflowWrap: "anywhere" }}
      >
        {children}
      </Typography>
    </Box>
  );
}

function Fields({ children }: { children: ReactNode }) {
  return <Box sx={{ display: "grid", gap: 2, width: "100%" }}>{children}</Box>;
}

function ServiceState({ enabled }: { enabled: boolean }) {
  return enabled ? (
    <Chip label="Enabled" color="success" size="small" />
  ) : (
    <Chip label="Disabled" size="small" />
  );
}

function Hint({ children }: { children: ReactNode }) {
  return (
    <Typography variant="body2" color="textSecondary">
      {children}
    </Typography>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <Box sx={{ textAlign: "center" }}>
      <Typography variant="h4">{value}</Typography>
      <Typography variant="body2" color="textSecondary">
        {label}
      </Typography>
    </Box>
  );
}

const THIRD = { xs: 12, md: 4 };

const HALF = { xs: 12, md: 6 };

const QUARTER = { xs: 12, sm: 6, md: 3 };

const TRAFFIC_THIRD = { xs: 12, sm: 6, md: 4 };

const failedToLoad = (q: { error: Error | null; data: unknown }) =>
  !!q.error && q.data === undefined;

export default function Dashboard() {
  const navigate = useNavigate();
  const auth = useAuth();

  const operator = useQuery({ queryKey: ["operator"], queryFn: getOperator });
  const sip = useQuery({ queryKey: ["sip"], queryFn: getSIPStatus });
  const peers = useQuery({
    queryKey: ["diameter-peers"],
    queryFn: listDiameterPeers,
    refetchInterval: DASHBOARD_REFRESH_MS,
  });
  const trunks = useQuery({
    queryKey: ["sip-trunks"],
    queryFn: listSIPTrunks,
    enabled: auth.can("sip_trunk:read"),
    refetchInterval: DASHBOARD_REFRESH_MS,
  });
  const hasTrunks = (trunks.data?.length ?? 0) > 0;
  const trafficSize = hasTrunks ? QUARTER : TRAFFIC_THIRD;
  const ipsmgw = useQuery({ queryKey: ["ip-sm-gw"], queryFn: getIPSMGW });
  const mcptt = useMCPTTStatus();
  const mcpttOn = !!mcptt.status;
  const pttClients = useQuery({
    queryKey: [...mcpttKeys.registrations, "count"],
    queryFn: countClientRegistrations,
    enabled: mcpttOn,
    refetchInterval: DASHBOARD_REFRESH_MS,
  });
  const pttCalls = useQuery({
    queryKey: [...mcpttKeys.calls, "count"],
    queryFn: countGroupCalls,
    enabled: mcpttOn,
    refetchInterval: DASHBOARD_REFRESH_MS,
  });
  const metrics = useQuery({
    queryKey: ["metrics"],
    queryFn: getMetrics,
    refetchInterval: DASHBOARD_REFRESH_MS,
  });
  const counts = useQuery({
    queryKey: ["call-records", "counts", "lastDay"],
    queryFn: () =>
      countCallRecords({ start: new Date(Date.now() - DAY_MS).toISOString() }),
    refetchInterval: COUNTS_REFRESH_MS,
  });

  const samples = metrics.data ?? [];
  const registered = sumOf(samples, "ellaims_registered_subscribers");
  const activeCalls = sumOf(samples, "ellaims_active_calls");
  const activeTrunkCalls = sumOf(samples, "ellaims_trunk_active_calls");
  const memory = sumOf(samples, "process_resident_memory_bytes");
  const database = sumOf(samples, "ellaims_database_storage_bytes");
  const startedAt = sumOf(samples, "process_start_time_seconds");
  const metricCard = {
    loading: metrics.isPending,
    error: failedToLoad(metrics),
  };
  const count = (n: number | undefined) =>
    n === undefined ? "N/A" : nf.format(n);

  const byOutcome = counts.data;
  const total = byOutcome
    ? CALL_OUTCOMES.reduce((sum, o) => sum + byOutcome[o], 0)
    : 0;
  const outcomes = byOutcome
    ? CALL_OUTCOMES.filter((o) => ALWAYS_SHOWN.includes(o) || byOutcome[o] > 0)
    : [];

  const smscs = (peers.data ?? []).filter(
    (p) => (p.service_centres ?? []).length > 0,
  );
  const pcscfs = sip.data ? pcscfAddresses(sip.data.listeners) : [];

  return (
    <Box component="section" aria-labelledby="dashboard-title">
      <PageHeader id="dashboard-title" title="Dashboard" />

      <Section title="Overview" first>
        <Grid size={hasTrunks ? HALF : { xs: 12, md: 8 }}>
          <Panel
            title="This IMS"
            to="/cores"
            loading={operator.isPending || sip.isPending}
            error={failedToLoad(operator) || failedToLoad(sip)}
            alignTop
          >
            <Fields>
              <Field label="Operator ID (MCC/MNC)">
                {operator.data && `${operator.data.mcc} / ${operator.data.mnc}`}
              </Field>
              <Field label="Home Domain">
                {sip.data && <DomainName name={sip.data.home_domain} />}
              </Field>
              <Field label="P-CSCF Addresses">
                {pcscfs.length > 0 ? (
                  <Box component="ul" sx={{ m: 0, p: 0, listStyle: "none" }}>
                    {pcscfs.map(({ ip }) => (
                      <li key={ip}>{ip}</li>
                    ))}
                  </Box>
                ) : (
                  "—"
                )}
              </Field>
            </Fields>
          </Panel>
        </Grid>
        <Grid size={hasTrunks ? QUARTER : THIRD}>
          <KpiCard
            title="Diameter Peers"
            to="/cores"
            loading={peers.isPending}
            error={failedToLoad(peers)}
            value={count(peers.data?.length)}
            alert={peers.data?.some((p) => p.status.state !== "open")}
          />
        </Grid>
        {hasTrunks && (
          <Grid size={QUARTER}>
            <KpiCard
              title="SIP Trunks"
              to="/sip-trunks"
              value={count(trunks.data?.length)}
              alert={trunks.data?.some((t) => t.status.state === "down")}
            />
          </Grid>
        )}
      </Section>

      <Section title="Traffic">
        <Grid size={trafficSize}>
          <KpiCard
            title="Registered Subscribers"
            hint="Subscribers with a registration that has not expired."
            to="/registrations"
            {...metricCard}
            value={count(registered)}
          />
        </Grid>
        <Grid size={trafficSize}>
          <KpiCard
            title="Active Calls"
            hint="Calls in progress, ringing or answered."
            to="/calls"
            {...metricCard}
            value={count(activeCalls)}
          />
        </Grid>
        {hasTrunks && (
          <Grid size={trafficSize}>
            <KpiCard
              title="Active Trunk Calls"
              to="/calls"
              {...metricCard}
              value={count(activeTrunkCalls)}
            />
          </Grid>
        )}
        <Grid size={trafficSize}>
          <Panel
            title="Calls in the Last 24 Hours"
            to={lastDayLink()}
            loading={counts.isPending}
            error={failedToLoad(counts)}
            alignTop
          >
            <TableContainer>
              <Table size="small" aria-label="Calls in the last 24 hours">
                <TableHead>
                  <TableRow>
                    <TableCell sx={headSx}>Outcome</TableCell>
                    <TableCell sx={headSx} align="right">
                      Calls
                    </TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {byOutcome &&
                    outcomes.map((outcome) => (
                      <TableRow
                        key={outcome}
                        hover
                        onClick={() => navigate(lastDayLink(outcome))}
                        sx={{ cursor: "pointer" }}
                      >
                        <TableCell>
                          <Box
                            component={RouterLink}
                            to={lastDayLink(outcome)}
                            onClick={(e) => e.stopPropagation()}
                            sx={{ textDecoration: "none" }}
                          >
                            <OutcomeChip outcome={outcome} />
                          </Box>
                        </TableCell>
                        <TableCell align="right">
                          {nf.format(byOutcome[outcome])}
                        </TableCell>
                      </TableRow>
                    ))}
                  <TableRow>
                    <TableCell sx={{ fontWeight: 600 }}>Total</TableCell>
                    <TableCell align="right" sx={{ fontWeight: 600 }}>
                      {nf.format(total)}
                    </TableCell>
                  </TableRow>
                </TableBody>
              </Table>
            </TableContainer>
          </Panel>
        </Grid>
      </Section>

      <Section title="Services">
        <Grid size={HALF}>
          <Panel
            title="SMS over IP"
            to="/cores"
            loading={ipsmgw.isPending || peers.isPending}
            error={failedToLoad(ipsmgw) || failedToLoad(peers)}
            action={
              ipsmgw.data && <ServiceState enabled={!!ipsmgw.data.number} />
            }
            alignTop
          >
            {ipsmgw.data?.number ? (
              <Fields>
                <Field label="IP-SM-GW Number">{ipsmgw.data.number}</Field>
                <Field label="SMSCs">
                  {smscs.length > 0 ? (
                    <Box
                      component="ul"
                      sx={{
                        m: 0,
                        p: 0,
                        listStyle: "none",
                        display: "grid",
                        gap: 0.5,
                      }}
                    >
                      {smscs.map((p) => (
                        <Stack
                          component="li"
                          key={p.id}
                          direction="row"
                          sx={{ alignItems: "center", gap: 1 }}
                        >
                          <span>
                            <DomainName name={peerName(p)} />
                          </span>
                          <PeerStateChip
                            state={p.status.state}
                            error={p.status.error}
                          />
                        </Stack>
                      ))}
                    </Box>
                  ) : (
                    <Chip label="no SMSC peer" color="error" size="small" />
                  )}
                </Field>
              </Fields>
            ) : (
              <Hint>No IP-SM-GW number: phones get no SMS over IP.</Hint>
            )}
          </Panel>
        </Grid>
        <Grid size={HALF}>
          <Panel
            title="Push-to-Talk"
            to="/ptt"
            loading={
              (mcptt.status === undefined && !mcptt.error) ||
              (mcpttOn && (pttClients.isPending || pttCalls.isPending))
            }
            error={
              (mcptt.status === undefined && !!mcptt.error) ||
              failedToLoad(pttClients) ||
              failedToLoad(pttCalls)
            }
            action={
              mcptt.status !== undefined && <ServiceState enabled={mcpttOn} />
            }
            alignTop={!mcpttOn}
          >
            {mcpttOn ? (
              <Box
                sx={{
                  display: "flex",
                  justifyContent: "space-around",
                  width: "100%",
                }}
              >
                <Stat
                  label="Registered Clients"
                  value={count(pttClients.data)}
                />
                <Stat label="Group Calls" value={count(pttCalls.data)} />
              </Box>
            ) : (
              <Hint>
                No <code>mcptt</code> section in <code>ims.yaml</code>.
              </Hint>
            )}
          </Panel>
        </Grid>
      </Section>

      <Section title="System">
        <Grid size={THIRD}>
          <KpiCard
            title="Process Memory"
            hint="RAM used by the IMS process (resident set size): what the operating system and a container memory limit count."
            {...metricCard}
            value={memory === undefined ? "N/A" : formatBytes(memory)}
          />
        </Grid>
        <Grid size={THIRD}>
          <KpiCard
            title="Database Size"
            hint="Size of the database on disk, its write-ahead log included."
            {...metricCard}
            value={database === undefined ? "N/A" : formatBytes(database)}
          />
        </Grid>
        <Grid size={THIRD}>
          <KpiCard
            title="Up Since"
            hint="Time when the IMS process was started."
            {...metricCard}
            value={
              startedAt === undefined
                ? "N/A"
                : formatDateTime(new Date(startedAt * 1000).toISOString())
            }
          />
        </Grid>
      </Section>
    </Box>
  );
}
