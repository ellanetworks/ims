import type { ReactNode } from "react";
import {
  Box,
  Card,
  type CardProps,
  CardActionArea,
  CardContent,
  CardHeader,
  Chip,
  type ChipProps,
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
import DomainName from "@/components/DomainName";
import PageHeader from "@/components/PageHeader";
import PeerStateChip from "@/components/PeerStateChip";
import { useAuth } from "@/contexts/AuthContext";
import { useMCPTTStatus } from "@/hooks/useMCPTT";
import { countCallRecords, type CallOutcome } from "@/queries/callRecords";
import {
  type ClusterHealth,
  type ClusterHealthState,
  getClusterHealth,
} from "@/queries/cluster";
import {
  type DiameterPeer,
  listDiameterPeers,
  peerName,
} from "@/queries/diameter";
import { getIPSMGW, listSMSRoutes } from "@/queries/ipsmgw";
import {
  countClientRegistrations,
  countGroupCalls,
  mcpttKeys,
} from "@/queries/mcptt";
import { getMetrics, sumOf } from "@/queries/metrics";
import { getOperator } from "@/queries/operator";
import { getSIPStatus } from "@/queries/sip";
import { listSIPTrunks, type SIPTrunk } from "@/queries/sipTrunks";
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

export const formatUptime = (seconds: number): string => {
  const s = Math.max(0, Math.floor(seconds));
  const days = Math.floor(s / 86_400);
  const hours = Math.floor((s % 86_400) / 3600);
  const minutes = Math.floor((s % 3600) / 60);
  if (days > 0) return `${days} d ${hours} h`;
  if (hours > 0) return `${hours} h ${minutes} min`;
  return `${minutes} min`;
};

interface OutcomeGroup {
  label: string;
  hint: string;
  outcomes: CallOutcome[];
  color: Exclude<ChipProps["color"], undefined>;
}

export const OUTCOME_GROUPS: OutcomeGroup[] = [
  {
    label: "Answered",
    hint: "The callee answered.",
    outcomes: ["answered"],
    color: "success",
  },
  {
    label: "Not Answered",
    hint: "The network set the call up, but it was not answered: no answer, busy, cancelled by the caller, or rejected by the callee.",
    outcomes: ["no_answer", "busy", "cancelled", "rejected"],
    color: "default",
  },
  {
    label: "Unreachable",
    hint: "The callee had no registration. Many of these can point to registration problems.",
    outcomes: ["unavailable"],
    color: "warning",
  },
  {
    label: "Failed",
    hint: "The call failed because of an error in the network.",
    outcomes: ["failed"],
    color: "error",
  },
];

const lastDayLink = (outcomes?: CallOutcome[]) =>
  `/calls?${outcomes ? `outcome=${outcomes.join(",")}&` : ""}range=24h`;

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
  compact?: boolean;
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
  compact,
  children,
  ...cardProps
}: PanelProps) {
  return (
    <Card
      {...cardProps}
      sx={{
        height: compact ? "auto" : "100%",
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
        sx={compact ? { ...headerSx, borderRadius: 3 } : headerSx}
      />
      {!compact && (
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
      )}
    </Card>
  );
}

interface KpiCardProps {
  title: string;
  hint?: string;
  value?: ReactNode;
  caption?: ReactNode;
  loading?: boolean;
  error?: boolean;
  to?: string;
}

function KpiCard({ title, hint, value, caption, to, ...state }: KpiCardProps) {
  const content = (
    <Box sx={{ textAlign: "center" }}>
      {typeof value === "string" ? (
        <Typography variant="h4">{value}</Typography>
      ) : (
        value
      )}
      {caption && (
        <Typography variant="body2" color="textSecondary" sx={{ mt: 1 }}>
          {caption}
        </Typography>
      )}
    </Box>
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
    <Box sx={{ minWidth: 0 }}>
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
  return (
    <Box
      sx={{
        display: "grid",
        gridTemplateColumns: "minmax(0, 1fr)",
        gap: 2,
        width: "100%",
        minWidth: 0,
      }}
    >
      {children}
    </Box>
  );
}

function ServiceState({ enabled }: { enabled: boolean }) {
  return enabled ? (
    <Chip label="Enabled" color="success" size="small" />
  ) : (
    <Chip label="Disabled" size="small" />
  );
}

function Stat({
  label,
  value,
  hint,
}: {
  label: string;
  value: string;
  hint?: string;
}) {
  const stat = (
    <Box sx={{ textAlign: "center" }}>
      <Typography variant="h4">{value}</Typography>
      <Typography variant="body2" color="textSecondary">
        {label}
      </Typography>
    </Box>
  );
  return hint ? (
    <Tooltip title={hint} arrow describeChild>
      {stat}
    </Tooltip>
  ) : (
    stat
  );
}

function Ratio({
  value,
  color,
}: {
  value: string;
  color?: "warning.main" | "error.main";
}) {
  return (
    <Typography variant="h4" sx={{ color }}>
      {value}
    </Typography>
  );
}

function ShareBar({
  share,
  color,
}: {
  share: number;
  color: OutcomeGroup["color"];
}) {
  return (
    <Box sx={{ display: "flex", alignItems: "center", gap: 1 }}>
      <Box
        aria-hidden
        sx={{
          flexGrow: 1,
          height: 8,
          borderRadius: 4,
          bgcolor: "action.hover",
          overflow: "hidden",
        }}
      >
        <Box
          sx={{
            width: `${share}%`,
            height: "100%",
            bgcolor: color === "default" ? "grey.500" : `${color}.main`,
          }}
        />
      </Box>
      <Typography
        variant="body2"
        color="textSecondary"
        sx={{ minWidth: 40, textAlign: "right" }}
      >
        {Math.round(share)}%
      </Typography>
    </Box>
  );
}

const SINGLE_NODE_HINT =
  "A single node. Add nodes to keep the IMS working when one fails.";

const HA_HINTS: Record<ClusterHealthState, string> = {
  healthy: "Healthy voters out of the cluster's total.",
  degraded: "At least one voter is unhealthy.",
  no_leader:
    "No node holds leadership. The cluster cannot accept writes until enough voters recover.",
  unknown: "Cluster health could not be read from the leader.",
};

function clusterHealth(health?: ClusterHealth): {
  hint: string;
  value: ReactNode;
} {
  if (!health || health.state === "unknown") {
    return { hint: HA_HINTS.unknown, value: <Chip label="Unknown" /> };
  }
  if (health.state === "no_leader") {
    return {
      hint: HA_HINTS.no_leader,
      value: <Chip label="No leader" color="error" />,
    };
  }
  const single = health.state === "healthy" && health.total_voters === 1;
  return {
    hint: single ? SINGLE_NODE_HINT : HA_HINTS[health.state],
    value: (
      <Ratio
        value={`${health.healthy_voters ?? 0}/${health.total_voters}`}
        color={health.state === "healthy" ? undefined : "error.main"}
      />
    ),
  };
}

function peersHealth(peers: DiameterPeer[]): {
  hint: string;
  value: ReactNode;
  caption?: string;
} {
  if (peers.length === 0) {
    return { hint: "No Diameter peers are configured.", value: "None" };
  }
  const notOpen = peers.filter((p) => p.status.state !== "open");
  const hss = peers.filter((p) => p.applications.includes("cx"));
  const hssOpen = hss.some((p) => p.status.state === "open");
  const color =
    notOpen.length === 0
      ? undefined
      : notOpen.length === peers.length || (hss.length > 0 && !hssOpen)
        ? "error.main"
        : "warning.main";
  const hssCaption =
    hss.length === 0 ? "No HSS peer" : hssOpen ? "HSS open" : "HSS down";
  return {
    hint:
      notOpen.length === 0
        ? "Peers with an open connection out of all peers."
        : `Not open: ${notOpen.map(peerName).join(", ")}.`,
    value: (
      <Ratio
        value={`${peers.length - notOpen.length}/${peers.length}`}
        color={color}
      />
    ),
    caption: `Open · ${hssCaption}`,
  };
}

function trunksHealth(trunks: SIPTrunk[]): {
  hint: string;
  value: ReactNode;
  caption?: string;
} {
  if (trunks.length === 0) {
    return { hint: "No SIP trunks are configured.", value: "None" };
  }
  const up = trunks.filter((t) => t.status.state === "up").length;
  const down = trunks.filter(
    (t) => t.status.state === "down" || t.status.state === "unusable",
  );
  const color =
    down.length === 0
      ? undefined
      : down.length === trunks.length
        ? "error.main"
        : "warning.main";
  return {
    hint:
      down.length === 0
        ? "Trunks answering keepalives out of all trunks."
        : `Down: ${down.map((t) => t.name).join(", ")}.`,
    value: <Ratio value={`${up}/${trunks.length}`} color={color} />,
    caption: "Up",
  };
}

const QUARTER = { xs: 12, sm: 6, md: 3 };

const THIRD = { xs: 12, md: 4 };

const TWO_THIRDS = { xs: 12, md: 8 };

const HALF = { xs: 12, md: 6 };

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
  const cluster = useQuery({
    queryKey: ["cluster-health"],
    queryFn: getClusterHealth,
    enabled: auth.can("cluster:read"),
    refetchInterval: DASHBOARD_REFRESH_MS,
  });
  const ipsmgw = useQuery({ queryKey: ["ip-sm-gw"], queryFn: getIPSMGW });
  const smsRoutes = useQuery({
    queryKey: ["sms-routes"],
    queryFn: listSMSRoutes,
  });
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
  const groups = OUTCOME_GROUPS.map((group) => ({
    ...group,
    calls: byOutcome
      ? group.outcomes.reduce((sum, o) => sum + byOutcome[o], 0)
      : 0,
  }));
  const total = groups.reduce((sum, g) => sum + g.calls, 0);

  const diameterHealth = peersHealth(peers.data ?? []);
  const trunkHealth = trunksHealth(trunks.data ?? []);
  const haHealth = clusterHealth(cluster.data);

  const smsOn = !!ipsmgw.data?.number;
  const smscs = (peers.data ?? []).filter((p) =>
    (smsRoutes.data ?? []).some((r) => r.peers.includes(p.id)),
  );
  const pcscfs = sip.data ? pcscfAddresses(sip.data.listeners) : [];

  return (
    <Box component="section" aria-labelledby="dashboard-title">
      <PageHeader id="dashboard-title" title="Dashboard" />

      <Section title="Overview" first>
        <Grid size={QUARTER}>
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
                {sip.data && (
                  <Box
                    component="span"
                    title={sip.data.home_domain}
                    sx={{
                      display: "block",
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                      whiteSpace: "nowrap",
                    }}
                  >
                    {sip.data.home_domain}
                  </Box>
                )}
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
        <Grid size={QUARTER}>
          <KpiCard
            title="Diameter Peers"
            to="/cores"
            hint={diameterHealth.hint}
            loading={peers.isPending}
            error={failedToLoad(peers)}
            value={diameterHealth.value}
            caption={diameterHealth.caption}
          />
        </Grid>
        {auth.can("sip_trunk:read") && (
          <Grid size={QUARTER}>
            <KpiCard
              title="SIP Trunks"
              to="/sip-trunks"
              hint={trunkHealth.hint}
              loading={trunks.isPending}
              error={failedToLoad(trunks)}
              value={trunkHealth.value}
              caption={trunkHealth.caption}
            />
          </Grid>
        )}
        {auth.can("cluster:read") && (
          <Grid size={QUARTER}>
            <KpiCard
              title="High Availability"
              to="/cluster"
              hint={haHealth.hint}
              loading={cluster.isPending}
              error={failedToLoad(cluster)}
              value={haHealth.value}
            />
          </Grid>
        )}
      </Section>

      <Section title="Calls">
        <Grid size={TWO_THIRDS}>
          <Panel
            title="Last 24 Hours"
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
                    <TableCell sx={{ ...headSx, width: "50%" }}>
                      Share
                    </TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {byOutcome &&
                    groups.map((group) => (
                      <TableRow
                        key={group.label}
                        hover
                        onClick={() => navigate(lastDayLink(group.outcomes))}
                        sx={{ cursor: "pointer" }}
                      >
                        <TableCell>
                          <Tooltip title={group.hint} arrow describeChild>
                            <Box
                              component={RouterLink}
                              to={lastDayLink(group.outcomes)}
                              onClick={(e) => e.stopPropagation()}
                              sx={{ textDecoration: "none" }}
                            >
                              <Chip
                                label={group.label}
                                color={group.color}
                                size="small"
                              />
                            </Box>
                          </Tooltip>
                        </TableCell>
                        <TableCell align="right">
                          {nf.format(group.calls)}
                        </TableCell>
                        <TableCell>
                          <ShareBar
                            share={total ? (100 * group.calls) / total : 0}
                            color={group.color}
                          />
                        </TableCell>
                      </TableRow>
                    ))}
                  <TableRow>
                    <TableCell sx={{ fontWeight: 600 }}>Total</TableCell>
                    <TableCell align="right" sx={{ fontWeight: 600 }}>
                      {nf.format(total)}
                    </TableCell>
                    <TableCell />
                  </TableRow>
                </TableBody>
              </Table>
            </TableContainer>
          </Panel>
        </Grid>
        <Grid size={THIRD}>
          <Panel title="Now" {...metricCard}>
            <Box
              sx={{
                display: "flex",
                justifyContent: "space-around",
                flexWrap: "wrap",
                gap: 2,
                width: "100%",
              }}
            >
              <Stat
                label="Registered"
                hint="Subscribers with a registration that has not expired."
                value={count(registered)}
              />
              <Stat
                label="Active Calls"
                hint="Calls in progress, ringing or answered."
                value={count(activeCalls)}
              />
              {(trunks.data?.length ?? 0) > 0 && (
                <Stat
                  label="Trunk Calls"
                  hint="Active calls through a SIP trunk, part of the active calls."
                  value={count(activeTrunkCalls ?? 0)}
                />
              )}
            </Box>
          </Panel>
        </Grid>
      </Section>

      <Section title="Services">
        <Grid size={HALF}>
          <Panel
            title="SMS over IP"
            to="/sms"
            loading={ipsmgw.isPending || peers.isPending || smsRoutes.isPending}
            error={
              failedToLoad(ipsmgw) ||
              failedToLoad(peers) ||
              failedToLoad(smsRoutes)
            }
            action={ipsmgw.data && <ServiceState enabled={smsOn} />}
            compact={ipsmgw.data !== undefined && !smsOn}
            alignTop
          >
            <Fields>
              <Field label="IP-SM-GW Number">{ipsmgw.data?.number}</Field>
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
                        sx={{
                          alignItems: "center",
                          justifyContent: "space-between",
                          gap: 1,
                        }}
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
            compact={mcptt.status !== undefined && !mcpttOn}
          >
            <Box
              sx={{
                display: "flex",
                justifyContent: "space-around",
                width: "100%",
              }}
            >
              <Stat label="Registered Clients" value={count(pttClients.data)} />
              <Stat label="Group Calls" value={count(pttCalls.data)} />
            </Box>
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
            title="Uptime"
            hint={
              startedAt === undefined
                ? "Time since the IMS process was started."
                : `Started ${formatDateTime(new Date(startedAt * 1000).toISOString())}.`
            }
            {...metricCard}
            value={
              startedAt === undefined
                ? "N/A"
                : formatUptime(metrics.dataUpdatedAt / 1000 - startedAt)
            }
          />
        </Grid>
      </Section>
    </Box>
  );
}
