module proctor/drivers/postgres

go 1.27.4

require (
	github.com/bezmoradi/proctor/protocol v0.0.0
	github.com/jackc/pgx/v5 v5.13.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
	golang.org/x/text v0.42.0 // indirect
)

replace github.com/bezmoradi/proctor/protocol => ../../protocol
