{{/* 统一 labels（spec-0.10 D2） */}}
{{- define "airush.labels" -}}
app.kubernetes.io/part-of: airush
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}

{{/* 组件 selector labels */}}
{{- define "airush.selector" -}}
app.kubernetes.io/name: {{ . }}
app.kubernetes.io/part-of: airush
{{- end }}

{{/* 安全上下文基线（§2.1：nonroot + 只读 rootfs + 降权） */}}
{{- define "airush.securityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
runAsNonRoot: true
runAsUser: 65532
capabilities:
  drop: ["ALL"]
{{- end }}

{{/* 内置 PG 连接串（storage.builtin 时供 console/migrate 使用） */}}
{{- define "airush.builtinPgUrl" -}}
postgres://postgres:{{ .Values.storage.pg.password }}@{{ .Release.Name }}-pg:5432/airush?sslmode=disable
{{- end }}
