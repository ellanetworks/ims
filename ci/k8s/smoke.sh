#!/usr/bin/env bash
set -euo pipefail

MODE="${MODE:-standalone}"
IMAGE="${IMAGE:-ella-ims:latest}"
PREVIOUS_IMAGE="${PREVIOUS_IMAGE:-}"

CLUSTER=ims-smoke
NODE="${CLUSTER}-control-plane"
NETWORK=ims-smoke
CORE=ims-smoke-core
N6="${N6_NET:-10.6.0}"
UE_POOL=10.46.0.0/16
NS=ella

MULTUS_VERSION=v4.3.1
CNI_PLUGINS_VERSION=v1.9.1

SUBS=20
CALL_SECS=30

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
WATCH_PID=""
FORWARD_PID=""

k() { kubectl -n "${NS}" "$@"; }

step() { echo "::group::$*"; }
endstep() { echo "::endgroup::"; }

fail() {
  echo "::error::$*"
  exit 1
}

cleanup() {
  status=$?
  [ -n "${WATCH_PID}" ] && kill "${WATCH_PID}" 2>/dev/null || true
  [ -n "${FORWARD_PID}" ] && kill "${FORWARD_PID}" 2>/dev/null || true
  if [ "${status}" -ne 0 ]; then
    echo "== Diagnostics"
    k get all,pvc,pdb,networkpolicy,network-attachment-definitions -o wide || true
    for pod in $(k get pods -o name 2>/dev/null); do
      k describe "${pod}" || true
      k logs "${pod}" --tail=100 || true
      k logs "${pod}" --previous --tail=100 2>/dev/null || true
    done
    kubectl -n kube-system logs -l app=multus --tail=50 || true
    grep -E 'load_linux_test|^(--- |PASS|FAIL|panic)' "${WORK}/load.log" 2>/dev/null | tail -40 || true
  fi
  if [ -z "${KEEP:-}" ]; then
    docker rm -f "${CORE}" >/dev/null 2>&1 || true
    kind delete cluster --name "${CLUSTER}" >/dev/null 2>&1 || true
    docker network rm "${NETWORK}" >/dev/null 2>&1 || true
  fi
  rm -rf "${WORK}"
  exit "${status}"
}
trap cleanup EXIT

case "${MODE}" in
standalone | ha) ;;
*) fail "MODE must be standalone or ha, not ${MODE}" ;;
esac

SIP_ADDR="${N6}.5"
CORE_ADDR="${N6}.2"
CORE_DIAMETER=10.3.0.2
NODE_N6_ADDR=""
API_PORT=5020

ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD="correct horse"
TOKEN=""

api() {
  local auth=()
  [ -z "${TOKEN}" ] || auth=(-H "Authorization: Bearer ${TOKEN}")
  curl -fsS --max-time 10 "${auth[@]}" "http://${SIP_ADDR}:${API_PORT}/api/v1$1" "${@:2}"
}

log_in() {
  local credentials jwt
  credentials="$(jq -n --arg email "${ADMIN_EMAIL}" --arg password "${ADMIN_PASSWORD}" '{email: $email, password: $password}')"
  TOKEN=""
  api /init -X POST -H 'Content-Type: application/json' -d "${credentials}" >/dev/null 2>&1 || true
  jwt="$(api /auth/login -X POST -H 'Content-Type: application/json' -d "${credentials}" | jq -er .result.token)" \
    || fail "cannot log in as ${ADMIN_EMAIL}"
  TOKEN="${jwt}"
  TOKEN="$(api /users/me/api-tokens -X POST -H 'Content-Type: application/json' -d '{"name": "smoke test"}' \
    | jq -er .result.token)" || fail "cannot create an API token"
}

node_port() { k get service "$1" -o jsonpath="{.spec.ports[?(@.name==\"$2\")].nodePort}"; }

node_ip() { kubectl get node "${NODE}" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}'; }

create_namespace() {
  kubectl create namespace "${NS}"
  kubectl label namespace "${NS}" pod-security.kubernetes.io/enforce=privileged pod-security.kubernetes.io/warn=privileged
}

wait_ready() {
  k rollout status statefulset/ella-ims --timeout=180s
  local deadline=$((SECONDS + 120)) uid deleting ready
  while [ "${SECONDS}" -lt "${deadline}" ]; do
    IFS='|' read -r uid deleting ready < <(k get pod ella-ims-0 \
      -o jsonpath='{.metadata.uid}|{.metadata.deletionTimestamp}|{.status.conditions[?(@.type=="Ready")].status}{"\n"}' 2>/dev/null || echo '||')
    if [ -n "${uid}" ] && [ "${uid}" != "${1:-}" ] && [ -z "${deleting}" ] && [ "${ready}" = True ]; then
      return 0
    fi
    sleep 1
  done
  fail "ella-ims-0 did not become ready"
}

wait_api() {
  for _ in $(seq 30); do
    api /status >/dev/null 2>&1 && return 0
    sleep 1
  done
  fail "the API does not answer at ${SIP_ADDR}:${API_PORT}"
}

check_sip() {
  k logs ella-ims-0 | grep '"msg":"ims started"' | tail -n 1 \
    | jq -e --arg want "pcscf ${SIP_ADDR}:5060" '.sip | index($want)' >/dev/null \
    || fail "the P-CSCF does not listen on ${SIP_ADDR}:5060"
  echo "P-CSCF on ${SIP_ADDR}:5060"
}

check_diameter() {
  local pod_ip
  pod_ip="$(k get pod ella-ims-0 -o jsonpath='{.status.podIP}')"
  k logs ella-ims-0 | grep '"msg":"ims started"' | tail -n 1 \
    | jq -e --arg want "${pod_ip}:3868" '.diameter | any(endswith(" " + $want))' >/dev/null \
    || fail "Diameter does not listen on the pod's address, ${pod_ip}:3868"
  echo "Diameter on ${pod_ip}:3868"
}

deploy() {
  docker tag "$1" "ella-ims:$2"
  kind load docker-image "ella-ims:$2" --name "${CLUSTER}"
  local old
  old="$(k get pod ella-ims-0 -o jsonpath='{.metadata.uid}' 2>/dev/null || true)"
  helm upgrade --install ella-ims "${ROOT}/charts/ella-ims" --namespace "${NS}" \
    --set image.repository=ella-ims --set image.tag="$2" --set service.type=NodePort \
    --set sip.address="${SIP_ADDR}/24" \
    --set-json "sip.networkAttachment.routes=[{\"dst\":\"${UE_POOL}\",\"gw\":\"${CORE_ADDR}\"}]"
  wait_ready "${old}"
  wait_api
}

calls() { api "/call-records?per_page=1&$1" | jq -er .result.total_count; }

load() {
  local since
  since="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  docker exec "${CORE}" sh -c 'ip xfrm state flush && ip xfrm policy flush'
  docker exec "${CORE}" /loadtest.test -test.run TestLoad -test.v -test.timeout 10m -load \
    -ims "${SIP_ADDR}" -ims-diameter "${NODE_N6_ADDR}" -ims-host-label ella-ims-0 -local "${CORE_DIAMETER}" -ue-net "${UE_POOL}" \
    -subs "${SUBS}" -reg-rates 10 -reg-step 5s -settle 1s \
    -cps 1 -call-step "${CALL_SECS}s" -hold 3s -rereg 0 >"${WORK}/load.log" 2>&1 \
    || fail "the load client failed"
  grep -E 'load_linux_test' "${WORK}/load.log" | cut -c 1-200
  grep -q "register at .*: ${SUBS} in .*, 0 failed" "${WORK}/load.log" || fail "not all ${SUBS} phones registered"
  local answered failed
  answered="$(calls "outcome=answered&start=${since}")"
  failed="$(calls "start=${since}&outcome=cancelled&outcome=busy&outcome=rejected&outcome=no_answer&outcome=unavailable&outcome=failed")"
  echo "${answered} calls answered, ${failed} not"
  [ "${answered}" -ge $((CALL_SECS / 2)) ] || fail "only ${answered} calls were answered"
  [ "${failed}" = 0 ] || fail "${failed} calls failed"
}

PODS=(ella-ims-0 ella-ims-1 ella-ims-2)
FORWARD_PORT=15020

forward() {
  if [ -n "${FORWARD_PID}" ]; then
    kill "${FORWARD_PID}" 2>/dev/null || true
    wait "${FORWARD_PID}" 2>/dev/null || true
  fi
  kubectl -n "${NS}" port-forward "pod/$1" "${FORWARD_PORT}:5020" >"${WORK}/forward.log" 2>&1 &
  FORWARD_PID=$!
  SIP_ADDR=127.0.0.1
  API_PORT="${FORWARD_PORT}"
  wait_api
}

wait_all_ready() {
  k rollout status statefulset/ella-ims --timeout=300s
  k wait --for=condition=Ready pod -l app.kubernetes.io/name=ella-ims --timeout=180s
}

initialize() {
  local credentials code
  credentials="$(jq -n --arg email "${ADMIN_EMAIL}" --arg password "${ADMIN_PASSWORD}" '{email: $email, password: $password}')"
  for _ in $(seq 60); do
    code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 -X POST -H 'Content-Type: application/json' \
      -d "${credentials}" "http://${SIP_ADDR}:${API_PORT}/api/v1/init" || true)"
    case "${code}" in
    201 | 403) return 0 ;;
    esac
    sleep 5
  done
  fail "cannot create the first admin on ella-ims-0, last answer ${code}"
}

forward_leader() {
  local leader=""
  forward ella-ims-0
  for _ in $(seq 30); do
    leader="$(api /cluster | jq -r '.result.members[] | select(.leader) | .diameter_label')"
    [ -n "${leader}" ] && break
    sleep 2
  done
  [ -n "${leader}" ] || fail "the cluster has no leader"
  echo "leader ${leader}"
  forward "${leader}"
}

check_members() {
  local cluster healthy=false
  for _ in $(seq 30); do
    cluster="$(api /cluster)"
    if echo "${cluster}" | jq -e '
      .result.leader_id == .result.node_id and .result.healthy and
      ([.result.members[] | select(.suffrage == "voter" and .healthy) | .diameter_label] | sort == ["ella-ims-0", "ella-ims-1", "ella-ims-2"])
    ' >/dev/null; then
      healthy=true
      break
    fi
    sleep 2
  done
  echo "${cluster}" | jq -c '.result.members[] | {node_id, address, diameter_host, suffrage, leader, healthy}' >&2
  [ "${healthy}" = true ] || fail "the cluster does not have the three pods as healthy voters"
  echo "${cluster}" | jq -e '.result.members | length == 3 and all(.address | test("^ella-ims-[0-2]\\.ella-ims-headless\\.ella\\.svc\\.cluster\\.local:7000$"))' >/dev/null \
    || fail "a member does not advertise its per-pod DNS name"
  echo "${cluster}" | jq -r '[.result.members[] | .diameter_label + "=" + .node_id] | sort | join(" ")'
}

check_cluster_ready() {
  for _ in $(seq 30); do
    curl -fsS --max-time 10 "http://${SIP_ADDR}:${API_PORT}/api/v1/readyz?cluster=true" >/dev/null 2>&1 && return 0
    sleep 2
  done
  curl -sS --max-time 10 "http://${SIP_ADDR}:${API_PORT}/api/v1/readyz?cluster=true" || true
  fail "the cluster is not ready"
}

check_sip_interface() {
  local pod
  for pod in "${PODS[@]}"; do
    k logs "${pod}" | grep '"msg":"ims started"' | tail -n 1 | jq -e '.sip | any(startswith("pcscf 10.6.0."))' >/dev/null \
      || fail "the P-CSCF of ${pod} does not listen on eth1"
  done
  echo "the P-CSCF of each pod listens on eth1"
}

ha_smoke() {
  step "Deploy ${IMAGE}"
  docker tag "${IMAGE}" ella-ims:ci
  kind load docker-image ella-ims:ci --name "${CLUSTER}"
  create_namespace
  k create secret generic ella-ims-cluster --from-literal=secret="$(head -c 32 /dev/urandom | base64)"
  k apply -f "${ROOT}/ci/k8s/ims-sip-nad.yaml"
  helm install ella-ims "${ROOT}/charts/ella-ims" --namespace "${NS}" \
    --set image.repository=ella-ims --set image.tag=ci --set replicas=3 --set cluster.secretName=ella-ims-cluster \
    --set sip.address= --set sip.networkAttachment.create=false --set sip.networkAttachment.name=ims-sip
  endstep

  step "Form the cluster and create the first admin on ella-ims-0"
  for _ in $(seq 60); do
    [ "$(k get pods -l app.kubernetes.io/name=ella-ims -o name | wc -l)" = 3 ] && break
    sleep 2
  done
  k wait --for=jsonpath='{.status.phase}'=Running pod -l app.kubernetes.io/name=ella-ims --timeout=180s
  forward ella-ims-0
  initialize
  wait_all_ready
  k get pods -o wide
  [ "$(k get pods -l app.kubernetes.io/name=ella-ims -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sort -u | wc -l)" = 3 ] \
    || fail "the pods do not run on three nodes"
  log_in
  local cluster_id members
  forward_leader
  cluster_id="$(api /cluster | jq -er .result.cluster_id)"
  members="$(check_members)"
  echo "${members}"
  check_cluster_ready
  for pod in "${PODS[@]}"; do
    forward "${pod}"
    [ "$(api /cluster | jq -r .result.cluster_id)" = "${cluster_id}" ] || fail "${pod} is in another cluster"
  done
  check_sip_interface
  endstep

  step "Rolling restart"
  k rollout restart statefulset/ella-ims
  wait_all_ready
  forward_leader
  local after
  after="$(check_members)"
  echo "${after}"
  [ "${after}" = "${members}" ] || fail "the members changed across the rolling restart"
  [ "$(api /cluster | jq -r .result.cluster_id)" = "${cluster_id}" ] || fail "the cluster changed across the rolling restart"
  check_cluster_ready
  api /users/me >/dev/null || fail "the API token is gone after the rolling restart"
  for pod in "${PODS[@]}"; do
    restarts="$(k get pod "${pod}" -o jsonpath='{.status.containerStatuses[0].restartCount}')"
    [ "${restarts}" = 0 ] || fail "${pod} restarted ${restarts} times"
  done
  check_sip_interface
  k get pods -o wide
  endstep

  step "Run the chart's tests"
  helm test ella-ims --namespace "${NS}" --logs --timeout 3m
  endstep
}

step "Lint the manifests"
version="$(cat "${ROOT}/version/VERSION")"
helm template ella-ims "${ROOT}/charts/ella-ims" --namespace "${NS}" \
  | kubeconform -strict -summary -skip NetworkAttachmentDefinition
helm template ella-ims "${ROOT}/charts/ella-ims" --namespace "${NS}" \
  --set replicas=3 --set cluster.secretName=ella-ims-cluster \
  --set sip.address= --set sip.networkAttachment.create=false --set sip.networkAttachment.name=ims-sip \
  | kubeconform -strict -summary
endstep

step "Create the cluster"
if [ "${MODE}" = ha ]; then
  cat >"${WORK}/kind.yaml" <<'KIND'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
- role: worker
- role: worker
- role: worker
KIND
  kind create cluster --name "${CLUSTER}" --config "${WORK}/kind.yaml" --wait 120s
else
  if ip -4 route show to match "${N6}.1" | grep -qv '^default'; then
    fail "${N6}.0/24 is already routed on this host; set N6_NET to a free /24"
  fi
  docker network create "${NETWORK}" --subnet "${N6}.0/24" --ip-range "${N6}.128/25" --gateway "${N6}.1" >/dev/null
  kind create cluster --name "${CLUSTER}" --wait 120s
  docker network connect "${NETWORK}" "${NODE}"
  docker exec "${NODE}" ip -br addr show eth1
  NODE_N6_ADDR="$(docker inspect "${NODE}" -f "{{(index .NetworkSettings.Networks \"${NETWORK}\").IPAddress}}")"
fi
endstep

step "Install the CNI plugins and Multus"
curl -fsSL "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-amd64-${CNI_PLUGINS_VERSION}.tgz" \
  | tar -xz -C "${WORK}" ./macvlan ./static ./bridge ./host-local
for node in $(kind get nodes --name "${CLUSTER}"); do
  for plugin in macvlan static bridge host-local; do
    docker cp "${WORK}/${plugin}" "${node}:/opt/cni/bin/${plugin}"
  done
done
curl -fsSL "https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/${MULTUS_VERSION}/deployments/multus-daemonset-thick.yml" \
  | sed "s|multus-cni:snapshot-thick|multus-cni:${MULTUS_VERSION}-thick|" \
  | kubectl apply -f -
kubectl -n kube-system rollout status daemonset/kube-multus-ds --timeout=180s
for node in $(kind get nodes --name "${CLUSTER}"); do
  for _ in $(seq 60); do
    docker exec "${node}" test -f /etc/cni/net.d/00-multus.conf && break
    sleep 2
  done
  docker exec "${node}" test -f /etc/cni/net.d/00-multus.conf || fail "Multus did not configure the CNI on ${node}"
done
endstep

if [ "${MODE}" = ha ]; then
  ha_smoke
  echo "Kubernetes smoke test passed"
  exit 0
fi

step "Check the Pod Security of the pod"
kubectl create namespace pss-check >/dev/null
kubectl label namespace pss-check pod-security.kubernetes.io/enforce=restricted >/dev/null
violations="$(helm template ella-ims "${ROOT}/charts/ella-ims" --namespace pss-check --skip-tests \
  | kubectl create -n pss-check --dry-run=server -f - -o name 2>&1 >/dev/null \
  | grep -o 'would violate PodSecurity.*' || true)"
echo "${violations}"
want='would violate PodSecurity "restricted:latest": unrestricted capabilities (container "ims" must not include "NET_ADMIN" in securityContext.capabilities.add), runAsNonRoot != true (pod or container "ims" must set securityContext.runAsNonRoot=true), runAsUser=0 (container "ims" must not set runAsUser=0)'
[ "${violations}" = "${want}" ] || fail "the pod breaks other rules of the restricted Pod Security Standard than NET_ADMIN and root"
kubectl delete namespace pss-check --wait=false >/dev/null
endstep

step "Start the core"
(cd "${ROOT}" && CGO_ENABLED=0 go test -c -o "${WORK}/loadtest.test" ./internal/loadtest)
node_image="$(docker inspect "${NODE}" -f '{{.Config.Image}}')"
docker run -d --name "${CORE}" --privileged --network "${NETWORK}" --ip "${N6}.2" --entrypoint sleep "${node_image}" infinity >/dev/null
docker exec "${CORE}" ip route add local "${UE_POOL}" dev lo
docker exec "${CORE}" ip addr add "${CORE_DIAMETER}/32" dev lo
docker exec "${NODE}" ip route add "${CORE_DIAMETER}/32" via "${CORE_ADDR}"
docker cp "${WORK}/loadtest.test" "${CORE}:/loadtest.test"
endstep

step "Create the namespace"
create_namespace
endstep

if [ -n "${PREVIOUS_IMAGE}" ]; then
  step "Deploy the previous release, ${PREVIOUS_IMAGE}"
  docker image inspect "${PREVIOUS_IMAGE}" >/dev/null 2>&1 || docker pull "${PREVIOUS_IMAGE}"
  deploy "${PREVIOUS_IMAGE}" previous
  log_in
  endstep

  step "Upgrade to ${IMAGE}"
  deploy "${IMAGE}" ci
  api /users/me >/dev/null || fail "the API token created before the upgrade is gone"
  endstep
else
  step "Deploy ${IMAGE}"
  deploy "${IMAGE}" ci
  log_in
  endstep
fi

step "Check the pod"
sleep 30
restarts="$(k get pod ella-ims-0 -o jsonpath='{.status.containerStatuses[0].restartCount}')"
[ "${restarts}" = 0 ] || fail "the IMS restarted ${restarts} times"
k get pod ella-ims-0 -o wide
endstep

step "Check the API through the Service"
api_port="$(node_port ella-ims http)"
running="$(curl -fsS --max-time 10 "http://$(node_ip):${api_port}/api/v1/status" | jq -r .result.version)"
[ "${running}" = "${version}" ] || fail "the IMS reports version ${running}, want ${version}"
echo "version ${running} at $(node_ip):${api_port}"
endstep

step "Check the SIP address"
check_sip
endstep

step "Run the chart's tests"
helm test ella-ims --namespace "${NS}" --logs --timeout 3m
endstep

step "Register ${SUBS} phones over IPsec at ${SIP_ADDR}, and call"
load
endstep

step "Restart the pod"
uid="$(k get pod ella-ims-0 -o jsonpath='{.metadata.uid}')"
k logs -f ella-ims-0 >"${WORK}/stopped.log" 2>&1 &
kubectl -n "${NS}" get pod ella-ims-0 -w -o jsonpath='{.metadata.uid} {.status.containerStatuses[0].state.terminated.exitCode}{"\n"}' \
  >"${WORK}/watch.txt" 2>&1 &
WATCH_PID=$!
k delete pod ella-ims-0 --wait=false
wait_ready "${uid}"
kill "${WATCH_PID}"
WATCH_PID=""
wait_api

grep -q '"msg":"ims stopping"' "${WORK}/stopped.log" || fail "the IMS did not log its shutdown"
codes="$(awk -v uid="${uid}" '$1 == uid && NF == 2 { print $2 }' "${WORK}/watch.txt" | sort -u)"
[ "${codes}" = 0 ] || fail "the IMS exited with code '${codes}', want 0"
tail -n 3 "${WORK}/stopped.log" | cut -c 1-300
check_sip
check_diameter
api /users/me >/dev/null || fail "the API token is gone after the restart"
endstep

step "Register the phones again, and call"
load
endstep

echo "Kubernetes smoke test passed"
