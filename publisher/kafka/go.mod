module github.com/getsyntegrity/urd/publisher/kafka

go 1.26.0

require (
	github.com/IBM/sarama v1.61.1
	github.com/getsyntegrity/go-specs v0.3.3
	github.com/getsyntegrity/urd v0.1.0
	github.com/pablogore/kit-logger v0.1.2-0.20260912231430-17d1a6eacc85
	go.uber.org/atomic v1.12.0
	google.golang.org/protobuf v1.36.12
)

require golang.org/x/sys v0.48.0 // indirect

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/eapache/go-resiliency v1.7.0 // indirect
	github.com/hashicorp/go-uuid v1.0.3 // indirect
	github.com/jcmturner/aescts/v2 v2.0.0 // indirect
	github.com/jcmturner/dnsutils/v2 v2.0.0 // indirect
	github.com/jcmturner/gofork v1.7.6 // indirect
	github.com/jcmturner/gokrb5/v8 v8.4.4 // indirect
	github.com/jcmturner/rpc/v2 v2.0.3 // indirect
	github.com/klauspost/compress v1.20.1 // indirect
	github.com/pierrec/lz4/v4 v4.1.31 // indirect
	github.com/prometheus/client_golang v1.24.1 // indirect
	github.com/rcrowley/go-metrics v0.0.0-20250401214520-65e299d6c5c9 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/time v0.16.0 // indirect
)

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
// (ActorOf, ActorExists and Kill panic with a nil pointer when an actor leaves the tree). Remove this
// replace, and require the GoAkt release that carries the fix, once that PR is released.
replace github.com/tochemey/goakt/v4 => github.com/pablogore/goakt/v4 v4.5.7-0.20261005195759-c51a72dca92f
