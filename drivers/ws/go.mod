module proctor/drivers/ws

go 1.27.1

require (
	github.com/coder/websocket v1.8.14
	proctor/protocol v0.0.0
)

replace proctor/protocol => ../../protocol
