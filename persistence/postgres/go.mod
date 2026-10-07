module github.com/getsyntegrity/urd/persistence/postgres

go 1.27.0

// Use the local urd module so this module always builds against the current source.
replace github.com/getsyntegrity/urd => ../../

require (
	github.com/getsyntegrity/go-specs v0.3.3
	github.com/getsyntegrity/urd v0.1.0
	github.com/jackc/pgx/v5 v5.11.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/text v0.38.0 // indirect
)

// TEMPORARY: the pinned GoAkt commit plus the fix of https://github.com/Tochemey/goakt/pull/1447
// (ActorOf, ActorExists and Kill panic with a nil pointer when an actor leaves the tree), tagged
// v4.5.7-actorof.1 on the pablogore/goakt fork. Remove this
// replace, and require the GoAkt release that carries the fix, once that PR is released.
replace github.com/tochemey/goakt/v4 => github.com/pablogore/goakt/v4 v4.5.7-actorof.1
