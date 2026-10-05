module github.com/getsyntegrity/urd/publisher/websocket

go 1.26.0

require (
	github.com/getsyntegrity/go-specs v0.3.3
	github.com/getsyntegrity/urd v0.1.0
	github.com/gorilla/websocket v1.5.3
	github.com/tochemey/gopack v0.2.1
	go.uber.org/atomic v1.12.0
	google.golang.org/protobuf v1.36.12
)

require go.uber.org/multierr v1.11.0 // indirect

// HashiCorp renamed github.com/armon/go-metrics to github.com/hashicorp/go-metrics
// in v0.4.2 and every release since declares the new module path, so they fail
// to satisfy the legacy import path that hashicorp/go-metrics/compat still
// pulls in transitively (via memberlist → goakt → urd). v0.4.1 is the last
// version that resolves under the armon path; exclude the broken ones so
// `go mod tidy` and `go get -u` stop probing them.
exclude (
	github.com/armon/go-metrics v0.4.2
	github.com/armon/go-metrics v0.5.0
	github.com/armon/go-metrics v0.5.1
	github.com/armon/go-metrics v0.5.2
	github.com/armon/go-metrics v0.5.3
	github.com/armon/go-metrics v0.5.4
	github.com/armon/go-metrics v0.6.0
	github.com/armon/go-metrics v0.6.1
)

replace github.com/getsyntegrity/urd => ../../

// TEMPORARY: the pinned GoAkt commit plus the fix of https://github.com/Tochemey/goakt/pull/1447
// (ActorOf, ActorExists and Kill panic with a nil pointer when an actor leaves the tree), tagged
// v4.5.7-actorof.1 on the pablogore/goakt fork. Remove this
// replace, and require the GoAkt release that carries the fix, once that PR is released.
replace github.com/tochemey/goakt/v4 => github.com/pablogore/goakt/v4 v4.5.7-actorof.1
