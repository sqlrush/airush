module github.com/sqlrush/airush/libs/llm

go 1.25.0

require (
	github.com/sqlrush/airush/libs/apierror v0.0.0-00010101000000-000000000000
	github.com/sqlrush/airush/libs/obs v0.0.0-00010101000000-000000000000
	github.com/sqlrush/airush/libs/tenancy v0.0.0-00010101000000-000000000000
)

replace github.com/sqlrush/airush/libs/apierror => ../apierror

replace github.com/sqlrush/airush/libs/obs => ../obs

replace github.com/sqlrush/airush/libs/tenancy => ../tenancy
