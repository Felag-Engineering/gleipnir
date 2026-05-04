package main

import "github.com/felag-engineering/gleipnir/plugin-sdk/serve"

func main() {
	// serve.Serve() is the last call in every plugin binary. It wires up the
	// go-plugin transport, registers ToolService, and blocks until the host
	// disconnects.
	//
	// This is a Phase-3 stub and panics if called directly. Run service_test.go
	// instead to exercise ToolService in-process via loopback TCP (127.0.0.1:0).
	serve.Serve()
}
