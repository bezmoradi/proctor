module proctor/drivers/redis

go 1.27.4

require (
	github.com/bezmoradi/proctor/protocol v0.0.0
	github.com/redis/go-redis/v9 v9.23.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)

replace github.com/bezmoradi/proctor/protocol => ../../protocol
