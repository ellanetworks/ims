#!/usr/bin/env bash
# Smoke test of the Kubernetes manifests in k8s/: deploys them, through the
# overlay ci/k8s/<MODE>, on a single-node kind cluster, and checks the IMS over
# its API Service, with phones that register and call over IPsec, and across a
# restart of the pod and, when PREVIOUS_IMAGE is set, an upgrade.
#
# A privileged container, the core, plays the core network with the load client
# of internal/loadtest: the HSS and the PCRF over Diameter, and the phones of
# the IP pool 10.46.0.0/16, whose addresses it takes as its own, as a UPF routes
# them.
#
# MODE multus deploys k8s/. The kind node gets a second interface, eth1, on a
# Docker network, N6, 10.6.0.0/24, whose link carries the Diameter network,
# 10.3.0.0/24, too, as both NADs have eth1 as master: the NADs and the ConfigMap
# are deployed as shipped. The core is 10.6.0.2, the gateway to the IP pool of
# the SIP NAD, and 10.3.0.2.
#
# MODE pod-network deploys k8s/pod-network/, with no Multus. The core is on the
# kind network, and routes the pod network through the node, which routes the
# IP pool through the core: the phones reach the pod's address without NAT.
#
# Needs docker, kind, kubectl, kubeconform, go, curl, jq and the sctp, esp4 and
# xfrm_user kernel modules. Environment:
#   MODE            multus or pod-network (multus)
#   IMAGE           the image under test, in the local Docker (ella-ims:latest)
#   PREVIOUS_IMAGE  an image to deploy first and upgrade from (none)
#   N6_NET          the first three octets of the N6 network (10.6.0), to
#                   change on a host that already has it
#   DIAMETER_NET    of the Diameter network (10.3.0)
#   KEEP            set to keep the cluster, the core and the network afterwards
set -euo pipefail

MODE="${MODE:-multus}"
IMAGE="${IMAGE:-ella-ims:latest}"
PREVIOUS_IMAGE="${PREVIOUS_IMAGE:-}"

CLUSTER=ims-smoke
NODE="${CLUSTER}-control-plane"
NETWORK=ims-smoke
CORE=ims-smoke-core
N6="${N6_NET:-10.6.0}"
DIAMETER="${DIAMETER_NET:-10.3.0}"
# The addresses of k8s/, in the NADs and the ConfigMap.
SHIPPED_N6=10.6.0
SHIPPED_DIAMETER=10.3.0
UE_POOL=10.46.0.0/16
NS=ella

MULTUS_VERSION=v4.3.1
CNI_PLUGINS_VERSION=v1.9.1

# The phones, and how long they make calls, at one per second.
SUBS=20
CALL_SECS=30

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
WATCH_PID=""

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
  if [ "${status}" -ne 0 ]; then
    echo "== Diagnostics"
    k get all,pvc,network-attachment-definitions -o wide || true
    k describe pod ella-ims-0 || true
    k logs ella-ims-0 --tail=100 || true
    k logs ella-ims-0 --previous --tail=100 2>/dev/null || true
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
multus | pod-network) ;;
*) fail "MODE must be multus or pod-network, not ${MODE}" ;;
esac

# Where this host reaches the API, and where the core reaches SIP and
# Diameter, set once the IMS is deployed.
API_ADDR=""
API_PORT=5020
SIP_ADDR=""
DIAMETER_ADDR=""

# The admin of internal/api/apitest, as which the load client logs in too, and
# the API token with which this script calls the API, set by log_in.
ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD="correct horse"
TOKEN=""

# The JSON result of an API call, with the API token once there is one.
api() {
  local auth=()
  [ -z "${TOKEN}" ] || auth=(-H "Authorization: Bearer ${TOKEN}")
  curl -fsS --max-time 10 "${auth[@]}" "http://${API_ADDR}:${API_PORT}/api/v1$1" "${@:2}"
}

# Creates the admin if the IMS has no user yet, logs it in, and creates the
# API token that api sends from then on. Unlike an access token, it outlives
# the smoke test.
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

# The manifests of the overlay, with the networks of this host.
manifests() {
  kubectl kustomize "${ROOT}/ci/k8s/${MODE}" \
    | sed -e "s/\b${SHIPPED_N6//./\\.}\./${N6}./g" -e "s/\b${SHIPPED_DIAMETER//./\\.}\./${DIAMETER}./g"
}

# Waits for ella-ims-0 to be ready, not being deleted, and another pod than
# the one of UID $1, if given: the replacement of a deleted pod has its name.
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

# Waits for the API to answer, through the address and port this host uses.
wait_api() {
  for _ in $(seq 30); do
    api /status >/dev/null 2>&1 && return 0
    sleep 1
  done
  fail "the API does not answer at ${API_ADDR}:${API_PORT}"
}

# Sets where the IMS is: its Multus addresses, or the pod's.
locate() {
  if [ "${MODE}" = multus ]; then
    SIP_ADDR="${N6}.5"
    DIAMETER_ADDR="${DIAMETER}.5"
    API_ADDR="${SIP_ADDR}"
  else
    SIP_ADDR="$(k get pod ella-ims-0 -o jsonpath='{.status.podIP}')"
    DIAMETER_ADDR="${SIP_ADDR}"
    API_ADDR="$(node_ip)"
    API_PORT="$(node_port ella-ims http)"
  fi
}

# Deploys the overlay with the given image, loaded into the cluster as ella-ims:ci.
deploy() {
  docker tag "$1" ella-ims:ci
  kind load docker-image ella-ims:ci --name "${CLUSTER}"
  manifests | kubectl apply -f -
  # The tag does not change between deploys, so restart to run the new image.
  local old=""
  if [ "${2:-}" = restart ]; then
    old="$(k get pod ella-ims-0 -o jsonpath='{.metadata.uid}')"
    k rollout restart statefulset/ella-ims
  fi
  wait_ready "${old}"
  locate
  wait_api
}

# The number of call records with the given query.
calls() { api "/call-records?per_page=1&$1" | jq -er .result.total_count; }

# Registers SUBS phones from the core, has them make calls for CALL_SECS, and
# checks that they were answered.
load() {
  local since
  since="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  docker exec "${CORE}" sh -c 'ip xfrm state flush && ip xfrm policy flush'
  docker exec "${CORE}" /loadtest.test -test.run TestLoad -test.v -test.timeout 10m -load \
    -ims "${SIP_ADDR}" -ims-diameter "${DIAMETER_ADDR}" -local "${CORE_DIAMETER}" -ue-net "${UE_POOL}" \
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

step "Lint the manifests"
version="$(cat "${ROOT}/version/VERSION")"
for dir in k8s k8s/pod-network ci/k8s/multus ci/k8s/pod-network; do
  kubectl kustomize "${ROOT}/${dir}" | kubeconform -strict -summary -skip NetworkAttachmentDefinition
done
endstep

step "Create the cluster"
if [ "${MODE}" = multus ]; then
  if ip -4 route show to match "${N6}.1" | grep -qv '^default'; then
    fail "${N6}.0/24 is already routed on this host; set N6_NET to a free /24"
  fi
  # Docker gives addresses from the upper half only, so those of the IMS and the core stay free.
  docker network create "${NETWORK}" --subnet "${N6}.0/24" --ip-range "${N6}.128/25" --gateway "${N6}.1" >/dev/null
fi
kind create cluster --name "${CLUSTER}" --wait 120s
if [ "${MODE}" = multus ]; then
  docker network connect "${NETWORK}" "${NODE}"
  docker exec "${NODE}" ip -br addr show eth1
fi
endstep

step "Check the Pod Security of the pod"
# Only the privileged Pod Security Standard allows NET_ADMIN, which IPsec takes,
# and root, to which Kubernetes gives it. The pod breaks no other rule of the
# restricted one.
kubectl create namespace pss-check >/dev/null
kubectl label namespace pss-check pod-security.kubernetes.io/enforce=restricted >/dev/null
# The pod of both variants is that of k8s/base, which has no namespace.
violations="$(kubectl kustomize "${ROOT}/k8s/base" | kubectl -n pss-check create --dry-run=server -f - -o name 2>&1 >/dev/null \
  | grep -o 'would violate PodSecurity.*' || true)"
echo "${violations}"
want='would violate PodSecurity "restricted:latest": unrestricted capabilities (container "ims" must not include "NET_ADMIN" in securityContext.capabilities.add), runAsNonRoot != true (pod or container "ims" must set securityContext.runAsNonRoot=true), runAsUser=0 (container "ims" must not set runAsUser=0)'
[ "${violations}" = "${want}" ] || fail "the pod breaks other rules of the restricted Pod Security Standard than NET_ADMIN and root"
kubectl delete namespace pss-check --wait=false >/dev/null
endstep

if [ "${MODE}" = multus ]; then
step "Install the CNI plugins and Multus"
curl -fsSL "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-amd64-${CNI_PLUGINS_VERSION}.tgz" \
  | tar -xz -C "${WORK}" ./macvlan ./static
docker cp "${WORK}/macvlan" "${NODE}:/opt/cni/bin/macvlan"
docker cp "${WORK}/static" "${NODE}:/opt/cni/bin/static"
curl -fsSL "https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/${MULTUS_VERSION}/deployments/multus-daemonset-thick.yml" \
  | sed "s|multus-cni:snapshot-thick|multus-cni:${MULTUS_VERSION}-thick|" \
  | kubectl apply -f -
kubectl -n kube-system rollout status daemonset/kube-multus-ds --timeout=180s
endstep
fi

step "Start the core"
(cd "${ROOT}" && CGO_ENABLED=0 go test -c -o "${WORK}/loadtest.test" ./internal/loadtest)
# The node's image has the tools the core needs: ip, and a shell.
node_image="$(docker inspect "${NODE}" -f '{{.Config.Image}}')"
if [ "${MODE}" = multus ]; then
  docker run -d --name "${CORE}" --privileged --network "${NETWORK}" --ip "${N6}.2" --entrypoint sleep "${node_image}" infinity >/dev/null
  docker exec "${CORE}" ip addr add "${DIAMETER}.2/24" dev eth0
  CORE_DIAMETER="${DIAMETER}.2"
else
  docker run -d --name "${CORE}" --privileged --network kind --entrypoint sleep "${node_image}" infinity >/dev/null
  CORE_DIAMETER="$(docker inspect "${CORE}" -f '{{.NetworkSettings.Networks.kind.IPAddress}}')"
  pod_cidr="$(kubectl get node "${NODE}" -o jsonpath='{.spec.podCIDR}')"
  docker exec "${CORE}" ip route add "${pod_cidr}" via "$(node_ip)"
  docker exec "${NODE}" ip route add "${UE_POOL}" via "${CORE_DIAMETER}"
fi
docker exec "${CORE}" ip route add local "${UE_POOL}" dev lo
docker cp "${WORK}/loadtest.test" "${CORE}:/loadtest.test"
endstep

if [ -n "${PREVIOUS_IMAGE}" ]; then
  step "Deploy the previous release, ${PREVIOUS_IMAGE}"
  docker image inspect "${PREVIOUS_IMAGE}" >/dev/null 2>&1 || docker pull "${PREVIOUS_IMAGE}"
  deploy "${PREVIOUS_IMAGE}"
  log_in
  endstep

  step "Upgrade to ${IMAGE}"
  deploy "${IMAGE}" restart
  # The API token is in the database.
  api /users/me >/dev/null || fail "the API token created before the upgrade is gone"
  endstep
else
  step "Deploy ${IMAGE}"
  deploy "${IMAGE}"
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
k logs ella-ims-0 | grep '"msg":"ims started"' | tail -n 1 \
  | jq -e --arg want "pcscf ${SIP_ADDR}:5060" '.sip | index($want)' >/dev/null \
  || fail "the P-CSCF does not listen on ${SIP_ADDR}:5060"
echo "P-CSCF on ${SIP_ADDR}:5060"
endstep

step "Register ${SUBS} phones over IPsec at ${SIP_ADDR}, and call"
load
endstep

step "Restart the pod"
uid="$(k get pod ella-ims-0 -o jsonpath='{.metadata.uid}')"
# The pod keeps its name, so its logs and exit code are gone once it is
# replaced: follow them from before the deletion.
k logs -f ella-ims-0 >"${WORK}/stopped.log" 2>&1 &
k get pod ella-ims-0 -w -o jsonpath='{.metadata.uid} {.status.containerStatuses[0].state.terminated.exitCode}{"\n"}' \
  >"${WORK}/watch.txt" 2>&1 &
WATCH_PID=$!
k delete pod ella-ims-0 --wait=false
wait_ready "${uid}"
kill "${WATCH_PID}"
WATCH_PID=""
before_ip="${SIP_ADDR}"
locate
wait_api

grep -q '"msg":"ims stopping"' "${WORK}/stopped.log" || fail "the IMS did not log its shutdown"
codes="$(awk -v uid="${uid}" '$1 == uid && NF == 2 { print $2 }' "${WORK}/watch.txt" | sort -u)"
[ "${codes}" = 0 ] || fail "the IMS exited with code '${codes}', want 0"
tail -n 3 "${WORK}/stopped.log" | cut -c 1-300
[ "${SIP_ADDR}" = "${before_ip}" ] || fail "the IMS moved from ${before_ip} to ${SIP_ADDR}: the phones still reach the old address"
# The API token is in the database.
api /users/me >/dev/null || fail "the API token is gone after the restart"
echo "IMS back at ${SIP_ADDR}"
endstep

# The IPsec security associations were in the pod's network namespace, gone with
# it: as after a restart of a P-CSCF, the phones register again (TS 24.229
# §4.14.2).
step "Register the phones again, and call"
load
endstep

echo "Kubernetes smoke test passed"
