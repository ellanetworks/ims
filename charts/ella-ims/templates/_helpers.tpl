{{- define "ella-ims.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "ella-ims.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "ella-ims.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "ella-ims.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ella-ims.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "ella-ims.labels" -}}
helm.sh/chart: {{ include "ella-ims.chart" . }}
{{ include "ella-ims.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/component: ims
app.kubernetes.io/part-of: ella-networks
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "ella-ims.ha" -}}
{{- if gt (int .Values.replicas) 1 }}true{{ end }}
{{- end }}

{{- define "ella-ims.headlessServiceName" -}}
{{- printf "%s-headless" (include "ella-ims.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "ella-ims.networkAttachmentName" -}}
{{- default (printf "%s-sip" (include "ella-ims.fullname" .)) .Values.sip.networkAttachment.name }}
{{- end }}

{{- define "ella-ims.networks" -}}
{{- if or .Values.sip.networkAttachment.create .Values.sip.networkAttachment.name }}
{{- $network := dict "name" (include "ella-ims.networkAttachmentName" .) "interface" .Values.sip.interface }}
{{- with .Values.sip.address }}
{{- $_ := set $network "ips" (list .) }}
{{- end }}
{{- list $network | toJson }}
{{- end }}
{{- end }}

{{- define "ella-ims.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "ella-ims.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "ella-ims.openshift" -}}
{{- if .Capabilities.APIVersions.Has "security.openshift.io/v1/SecurityContextConstraints" }}true{{ end }}
{{- end }}

{{- define "ella-ims.openshiftSCC" -}}
{{- $mode := toString .Values.openshift.securityContextConstraints.create }}
{{- if or (eq $mode "true") (and (eq $mode "auto") (include "ella-ims.openshift" .)) }}true{{ end }}
{{- end }}

{{- define "ella-ims.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag }}
{{- if .Values.image.digest }}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest }}
{{- else }}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}
{{- end }}

{{- define "ella-ims.validate" -}}
{{- if include "ella-ims.ha" . }}
{{- if not .Values.cluster.secretName }}
{{- fail "replicas > 1 needs cluster.secretName, the name of a Secret with the cluster secret in its \"secret\" key" }}
{{- end }}
{{- if .Values.sip.address }}
{{- fail "replicas > 1 needs an empty sip.address and a NetworkAttachmentDefinition, in sip.networkAttachment.name, that gives each pod its own address" }}
{{- end }}
{{- end }}
{{- if and .Values.sip.networkAttachment.create (not .Values.sip.address) }}
{{- fail "sip.networkAttachment.create needs sip.address" }}
{{- end }}
{{- end }}

{{- define "ella-ims.config" -}}
{{- $user := deepCopy .Values.config }}
{{- $base := dict
  "db" (dict "path" "/data/ims.db")
  "api" (dict "address" "0.0.0.0" "port" 5020)
}}
{{- if not (dig "sip" "addresses" nil $user) }}
{{- $_ := set $base "sip" (dict "name" .Values.sip.interface) }}
{{- end }}
{{- if not (dig "diameter" "address" nil $user) }}
{{- $_ := set $base "diameter" (dict "name" "eth0") }}
{{- end }}
{{- if include "ella-ims.ha" . }}
{{- $join := printf "%s.%s.svc.%s:7000" (include "ella-ims.headlessServiceName" .) .Release.Namespace .Values.clusterDomain }}
{{- $_ := set $base "cluster" (dict "address" "0.0.0.0" "port" 7000 "join" (list $join) "bootstrap_expect" (int .Values.replicas) "secret_file" "/secrets/cluster/secret") }}
{{- end }}
{{- if .Values.api.tls.secretName }}
{{- $_ := set $base.api "tls" (dict "cert" "/tls/api/tls.crt" "key" "/tls/api/tls.key") }}
{{- end }}
{{- toYaml (mustMergeOverwrite $base $user) }}
{{- end }}

{{- define "ella-ims.apiScheme" -}}
{{- if .Values.api.tls.secretName }}HTTPS{{ else }}HTTP{{ end }}
{{- end }}
