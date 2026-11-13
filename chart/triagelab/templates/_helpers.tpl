{{/* Standard naming helpers. */}}
{{- define "triagelab.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "triagelab.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- default (printf "%s-%s" .Release.Name .Chart.Name) .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "triagelab.labels" -}}
app.kubernetes.io/name: triagelab
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "triagelab.serviceAccountName" -}}
{{- if and .Values.serviceAccount.create .Values.serviceAccount.name -}}
{{- .Values.serviceAccount.name -}}
{{- else if .Values.serviceAccount.name -}}
{{- .Values.serviceAccount.name -}}
{{- else -}}
{{- include "triagelab.fullname" . -}}
{{- end -}}
{{- end -}}

{{- define "triagelab.tokenSecretName" -}}
{{- if .Values.token.secretName -}}
{{- .Values.token.secretName -}}
{{- else -}}
{{- include "triagelab.fullname" . -}}-token
{{- end -}}
{{- end -}}

{{- define "triagelab.tokenSecretKey" -}}
{{- .Values.token.key | default .Values.config.token_env -}}
{{- end -}}

{{- define "triagelab.ticketsTokenSecretName" -}}
{{- if .Values.tickets.token.secretName -}}
{{- .Values.tickets.token.secretName -}}
{{- else -}}
{{- include "triagelab.fullname" . -}}-tickets-token
{{- end -}}
{{- end -}}

{{- define "triagelab.ticketsTokenSecretKey" -}}
{{- .Values.tickets.token.key | default .Values.tickets.config.token_env -}}
{{- end -}}

{{- define "triagelab.authSecretTokenName" -}}
{{- if .Values.auth.secret.secretName -}}
{{- .Values.auth.secret.secretName -}}
{{- else -}}
{{- include "triagelab.fullname" . -}}-auth-token
{{- end -}}
{{- end -}}

{{- define "triagelab.authSecretTokenKey" -}}
{{- .Values.auth.secret.key | default .Values.auth.config.oidc.client_secret_env -}}
{{- end -}}

{{- define "triagelab.telemetryHeadersSecretName" -}}
{{- if .Values.telemetry.headers.secretName -}}
{{- .Values.telemetry.headers.secretName -}}
{{- else -}}
{{- include "triagelab.fullname" . -}}-telemetry-headers
{{- end -}}
{{- end -}}

{{- define "triagelab.telemetryHeadersSecretKey" -}}
{{- .Values.telemetry.headers.key | default "headers" -}}
{{- end -}}

{{/* Render-time configuration checks so mistakes fail at install, not as a
     crash-looping pod. */}}
{{- define "triagelab.checks" -}}
{{- if and .Values.gitops.enabled (or (lt (int .Values.gitops.pollSeconds) 5) (gt (int .Values.gitops.pollSeconds) 300)) -}}
{{- fail "gitops.pollSeconds must be between 5 and 300" -}}
{{- end -}}
{{- if gt (len .Values.gitops.secretNames) 8 -}}
{{- fail "gitops.secretNames permits at most eight named Secrets" -}}
{{- end -}}
{{- if gt (int .Values.replicaCount) 1 -}}
{{- fail "replicaCount must stay at 1: the workspace is single-user with in-memory active-run and ticket state" -}}
{{- end -}}
{{- if and .Values.tickets.enabled (not .Values.tickets.config) -}}
{{- fail "tickets.enabled requires tickets.config" -}}
{{- end -}}
{{- if not (or .Values.token.secretName .Values.token.create) -}}
{{- fail "provide token.secretName of an existing Secret, or token.create with token.value" -}}
{{- end -}}
{{- if and (not .Values.token.secretName) .Values.token.create (not .Values.token.value) -}}
{{- fail "token.create requires token.value, or set token.secretName instead" -}}
{{- end -}}
{{- if and .Values.tickets.enabled .Values.tickets.config.token_env (not (or .Values.tickets.token.secretName .Values.tickets.token.create)) -}}
{{- fail "tickets.config.token_env requires tickets.token.secretName or tickets.token.create with a value" -}}
{{- end -}}
{{- if and .Values.auth.enabled (not .Values.auth.config) -}}
{{- fail "auth.enabled requires auth.config" -}}
{{- end -}}
{{- if and .Values.auth.enabled .Values.auth.config.oidc .Values.auth.config.oidc.client_secret_env (not (or .Values.auth.secret.secretName (and .Values.auth.secret.create .Values.auth.secret.value))) -}}
{{- fail "auth.config.oidc.client_secret_env references a Secret key; provide auth.secret.secretName, or auth.secret.create with auth.secret.value" -}}
{{- end -}}
{{- if and .Values.telemetry.enabled (not .Values.telemetry.config) -}}
{{- fail "telemetry.enabled requires telemetry.config" -}}
{{- end -}}
{{- if and .Values.telemetry.enabled .Values.telemetry.config.headers_env (not (or .Values.telemetry.headers.secretName .Values.telemetry.headers.create)) -}}
{{- fail "telemetry.config.headers_env requires telemetry.headers.secretName or telemetry.headers.create with a value" -}}
{{- end -}}
{{- end -}}
