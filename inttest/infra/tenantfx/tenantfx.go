// MIT License
//
// Copyright (c) 2022-2026 Arsene Tochemey Gandote
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

// Package tenantfx is the test-only fixture of the cross-tenant conformance flows (inttest/flows/tenancy).
//
// It starts no container: it takes the DSN of a database that infra/postgres already created. It gives the
// flows three things and nothing else: a caller-driven tenant resolver, a node (an actor system plus an engine
// on that database) that can be stopped and replaced to prove recovery, and a read-only view of the rows the
// engine wrote, so a flow asserts where a record landed and not only what a command returned.
//
// It never invents a production contract. It only wires public engine options (WithTenantResolver, WithTenant)
// and reads the events_store table the postgres adapter already owns.
package tenantfx

import (
	"context"
	"sync"

	"github.com/getsyntegrity/go-specs/specs"
	"github.com/google/uuid"
	goakt "github.com/tochemey/goakt/v4/actor"

	"github.com/getsyntegrity/urd/engine"
	"github.com/getsyntegrity/urd/persistence/postgres"
	"github.com/getsyntegrity/urd/tenancy"
)

type callerKey struct{}
type administrativeKey struct{}

// Resolver resolves the tenant of a call from its context, which is how an application's request-scoped tenant
// reaches the engine. A context built with Caller resolves to that tenant, one built with Administrative to an
// administrative context, and any other context fails to resolve, so the engine must reject it.
type Resolver struct{}

var _ tenancy.TenantResolver = Resolver{}

// Resolve implements tenancy.TenantResolver.
func (Resolver) Resolve(ctx context.Context) (tenancy.TenantContext, error) {
	if ctx.Value(administrativeKey{}) != nil {
		admin, err := tenancy.NewAdministrative("ops-team", "conformance")
		if err != nil {
			return tenancy.TenantContext{}, err
		}
		return tenancy.NewAdministrativeContext(admin)
	}
	id, _ := ctx.Value(callerKey{}).(string)
	tid, err := tenancy.NewTenantID(id)
	if err != nil {
		return tenancy.TenantContext{}, err
	}
	return tenancy.NewTenantContext(tid)
}

// Caller returns a context that Resolver resolves to tenant.
func Caller(tenant string) context.Context {
	return context.WithValue(context.Background(), callerKey{}, tenant)
}

// Administrative returns a context that Resolver resolves to an administrative (non-tenant) context.
func Administrative() context.Context {
	return context.WithValue(context.Background(), administrativeKey{}, true)
}

// Spawn declares the tenant an entity is spawned for.
func Spawn(tenant string) engine.SpawnOption { return engine.WithTenant(tenancy.TenantID(tenant)) }

// Node is one running Urd process on a database: an actor system, the engine in it and the Postgres store.
type Node struct {
	Engine *engine.Engine

	store    *postgres.EventStore
	system   goakt.ActorSystem
	stopOnce sync.Once
}

// StartNode starts a node on the database at dsn. The engine gets WithSchemaMigration, so the first node
// creates the schema and later nodes find it current, plus opts. Pass engine.WithTenantResolver(Resolver{})
// for a multi-tenant node, tenancy.WithSingleTenant for single-tenant mode, or nothing for a legacy node.
// The actor system has a unique name and no remoting, so parallel tests share no name and no port.
func StartNode(sc *specs.Context, dsn string, opts ...engine.Option) *Node {
	sc.Helper()
	ctx := context.Background()

	n := &Node{store: postgres.NewEventStore(dsn)}
	// Registered before anything can fail, so a half-started node is still torn down. Stop is idempotent.
	sc.Cleanup(func() { n.Stop(sc) })
	sc.Expect(n.store.Connect(ctx)).To(specs.BeNil())

	all := append([]engine.Option{engine.WithSchemaMigration(), engine.WithLogger(engine.DiscardLogger)}, opts...)
	cfg := engine.NewConfig(n.store, all...)
	system, err := goakt.NewActorSystem("tenantfx-"+uuid.NewString(), cfg.GoaktOptions()...)
	sc.Expect(err).To(specs.BeNil())
	n.system = system
	sc.Expect(system.Start(ctx)).To(specs.BeNil())

	eng, err := engine.NewEngine(system, cfg)
	sc.Expect(err).To(specs.BeNil())
	n.Engine = eng
	sc.Expect(eng.Start(ctx)).To(specs.BeNil())
	return n
}

// Stop shuts the engine, the actor system and the store down, once. Afterwards nothing of the node is alive,
// so a following node can only learn the state from the database.
func (n *Node) Stop(sc *specs.Context) {
	sc.Helper()
	n.stopOnce.Do(func() {
		ctx := context.Background()
		if n.Engine != nil {
			sc.Expect(n.Engine.Stop(ctx)).To(specs.BeNil())
		}
		if n.system != nil {
			sc.Expect(n.system.Stop(ctx)).To(specs.BeNil())
		}
		sc.Expect(n.store.Disconnect(ctx)).To(specs.BeNil())
	})
}
