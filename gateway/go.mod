module github.com/sqlrush/airush/gateway

go 1.25.0

require (
	github.com/sqlrush/airush/libs/config v0.0.0-00010101000000-000000000000
	go.opentelemetry.io/otel/trace v1.45.0
)

require (
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/bridges/otelslog v0.20.0 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.21.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.45.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.45.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.45.0 // indirect
	go.opentelemetry.io/otel/log v0.21.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/sdk v1.45.0 // indirect
	go.opentelemetry.io/otel/sdk/log v0.21.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.45.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

require (
	github.com/joho/godotenv v1.5.1 // indirect
	github.com/sqlrush/airush/libs/apierror v0.0.0-00010101000000-000000000000
	github.com/sqlrush/airush/libs/obs v0.0.0-00010101000000-000000000000
)

replace github.com/sqlrush/airush/libs/config => ../libs/config

replace github.com/sqlrush/airush/libs/obs => ../libs/obs

replace github.com/sqlrush/airush/libs/apierror => ../libs/apierror

require (
	github.com/sqlrush/airush/proto/gen/go v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.83.0
)

replace github.com/sqlrush/airush/proto/gen/go => ../proto/gen/go
