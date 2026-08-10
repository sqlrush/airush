module github.com/sqlrush/airush/agent-runtime

go 1.23

require github.com/sqlrush/airush/libs/config v0.0.0-00010101000000-000000000000

require github.com/joho/godotenv v1.5.1 // indirect

replace github.com/sqlrush/airush/libs/config => ../libs/config
