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

package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/persistence"
	"github.com/getsyntegrity/urd/tenancy"
)

// publicationScope returns the scope the engine's publishers and subscribers
// register for (EGO-TENANT-005). Without a tenant resolver it is
// persistence.Unscoped(), so single-tenant mode needs no plumbing. In a
// tenant-aware engine it is the resolver's fixed tenant; when the resolver has
// none, it fails closed with ErrPublicationTenantUndetermined, because the
// alternatives are an all-tenants subscription (a bypass, which #96 rules out)
// or a silent Unscoped() one that would receive nothing.
func (engine *Engine) publicationScope() (persistence.Scope, error) {
	if engine.tenantResolver == nil {
		return persistence.Unscoped(), nil
	}
	id, ok := tenancy.FixedTenantOf(engine.tenantResolver)
	if !ok {
		return persistence.Scope{}, ErrPublicationTenantUndetermined
	}
	scope, err := persistence.NewTenantScope(id)
	if err != nil {
		return persistence.Scope{}, errors.Join(ErrPublicationTenantUndetermined, err)
	}
	return scope, nil
}

// tenantPublicationScope returns the scope for an explicit per-tenant
// registration. It fails closed with ErrInvalidPublicationTenant for an empty or
// invalid id, and with ErrPublicationTenantMismatch when the engine is not
// tenant-aware (its traffic is Unscoped(), which a tenant registration could
// never receive) or its resolver fixes a different tenant.
func (engine *Engine) tenantPublicationScope(id tenancy.TenantID) (persistence.Scope, error) {
	validated, err := tenancy.NewTenantID(string(id))
	if err != nil {
		return persistence.Scope{}, errors.Join(ErrInvalidPublicationTenant, err)
	}
	if engine.tenantResolver == nil {
		return persistence.Scope{}, fmt.Errorf("%w: the engine has no tenant resolver", ErrPublicationTenantMismatch)
	}
	if fixed, ok := tenancy.FixedTenantOf(engine.tenantResolver); ok && fixed != validated {
		return persistence.Scope{}, fmt.Errorf("%w: the engine is fixed to another tenant", ErrPublicationTenantMismatch)
	}
	scope, err := persistence.NewTenantScope(validated)
	if err != nil {
		return persistence.Scope{}, errors.Join(ErrInvalidPublicationTenant, err)
	}
	return scope, nil
}

// deliveryContext builds the context a publisher receives for a message
// published for scope, and re-checks the message's own tenant identity against
// it (defence in depth: the publish site already checked it). For a tenant
// scope the context carries the tenant, so tenancy.From(ctx) names it. For
// Unscoped() it is a plain context, with no tenant plumbing. An invalid scope,
// or identity that is absent, invalid, administrative or for another tenant,
// returns an error and the message must not be delivered.
func deliveryContext(scope eventstream.Scope, tenantMetadata map[string]string) (context.Context, error) {
	if err := eventstream.VerifyScope(scope, tenantMetadata); err != nil {
		return nil, err
	}
	if scope.IsUnscoped() {
		return context.Background(), nil
	}
	tc, err := tenancy.NewTenantContext(scope.TenantID())
	if err != nil {
		return nil, err
	}
	return tenancy.Attach(context.Background(), tc)
}

// rejectPublication drops, logs at error level and counts a message whose
// tenant identity failed the delivery check. The publisher loop goes on.
func (engine *Engine) rejectPublication(publisherID, persistenceID string, scope eventstream.Scope, err error) {
	engine.metrics.PublicationRejected(context.Background())
	engine.logger.Error("publication dropped: tenant identity check failed",
		"publisher", publisherID,
		"persistence_id", persistenceID,
		"scope", scope.String(),
		"error", err)
}

type eventsStream struct {
	publisher  EventPublisher
	subscriber eventstream.Subscriber
	done       chan Done
}

type statesStream struct {
	publisher  StatePublisher
	subscriber eventstream.Subscriber
	done       chan Done
}

// Subscribe creates an events' subscriber.
//
// This function initializes a new subscriber for the event stream managed by the Urd engine. The subscriber
// will receive events from the topics specified by the engine's configuration.
//
// Returns:
//   - An eventstream.Subscriber instance that can be used to receive events.
//   - An error if the engine has not started or if there is an issue creating the subscriber.
func (engine *Engine) Subscribe() (eventstream.Subscriber, error) {
	if !engine.Started() {
		return nil, ErrEngineNotStarted
	}

	scope, err := engine.publicationScope()
	if err != nil {
		return nil, err
	}

	return engine.subscribeForScope(scope)
}

// SubscribeForTenant is Subscribe for exactly tenant id, in a tenant-aware
// engine whether or not its resolver fixes a tenant (EGO-TENANT-005). The
// subscriber receives only that tenant's events and states: never another
// tenant's, an unscoped one, or one with administrative metadata. The
// validation, the errors (ErrInvalidPublicationTenant,
// ErrPublicationTenantMismatch, ErrEngineNotStarted) and the absence of any
// wildcard or administrative form are those of AddEventPublishersForTenant. The
// subscriber ends when Engine.Stop closes the stream.
func (engine *Engine) SubscribeForTenant(id tenancy.TenantID) (eventstream.Subscriber, error) {
	if !engine.Started() {
		return nil, ErrEngineNotStarted
	}

	scope, err := engine.tenantPublicationScope(id)
	if err != nil {
		return nil, err
	}

	return engine.subscribeForScope(scope)
}

// subscribeForScope subscribes a new subscriber to the events and states topics
// for scope, registering nothing on failure.
func (engine *Engine) subscribeForScope(scope persistence.Scope) (eventstream.Subscriber, error) {
	engine.mutex.RLock()
	eventStream := engine.eventStream
	engine.mutex.RUnlock()

	subscriber := eventStream.AddSubscriber()
	for _, topic := range []string{protocol.EventsTopic, protocol.StatesTopic} {
		if err := protocol.SubscribeScoped(eventStream, subscriber, scope, topic); err != nil {
			eventStream.RemoveSubscriber(subscriber)
			return nil, err
		}
	}

	return subscriber, nil
}

// duplicatePublisherIDs checks a batch of publisher IDs of one kind before
// any of them is registered. It returns an error wrapping
// ErrDuplicatePublisherID that names, in batch order and once each, every ID
// already registered (as reported by registered) or repeated in the batch;
// otherwise nil.
func duplicatePublisherIDs(ids []string, registered func(id string) bool) error {
	seen := make(map[string]struct{}, len(ids))
	reported := make(map[string]struct{})
	var duplicates []string
	for _, id := range ids {
		_, repeated := seen[id]
		seen[id] = struct{}{}
		if !repeated && !registered(id) {
			continue
		}
		if _, done := reported[id]; done {
			continue
		}
		reported[id] = struct{}{}
		duplicates = append(duplicates, strconv.Quote(id))
	}
	if len(duplicates) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrDuplicatePublisherID, strings.Join(duplicates, ", "))
}

// AddEventPublishers registers one or more event publishers with the Urd engine.
// This function subscribes the publishers to the event stream, allowing them to receive events.
//
// Note: Event publishers are responsible for publishing events to external systems. They need to be added to the engine before processing any events.
//
// Parameters:
//   - publishers: A list of event publishers to be added to the engine.
//
// Returns ErrEngineNotStarted if the engine has not started, and an error
// wrapping ErrDuplicatePublisherID, naming the duplicate IDs, when a
// publisher ID is already registered for this kind or repeated in the call;
// in that case no publisher from the call is registered or started.
func (engine *Engine) AddEventPublishers(publishers ...EventPublisher) error {
	if !engine.Started() {
		return ErrEngineNotStarted
	}

	scope, err := engine.publicationScope()
	if err != nil {
		return err
	}

	return engine.registerEventPublishers(scope, publishers)
}

// AddEventPublishersForTenant is AddEventPublishers for exactly tenant id, in a
// tenant-aware engine whether or not its resolver fixes a tenant (EGO-TENANT-005).
//
// id is validated with tenancy.NewTenantID; an empty or invalid id returns an
// error matching ErrInvalidPublicationTenant. A publisher registered here
// receives only that tenant's publications, in a context for which
// tenancy.From names the tenant; it never receives another tenant's, an
// unscoped one, or one with administrative metadata. There is no wildcard, no
// empty id meaning "all", and no administrative or Unscoped() fallback.
//
// The engine must be tenant-aware, and when its resolver fixes a tenant, id must
// be that tenant: otherwise the call returns ErrPublicationTenantMismatch. It
// returns ErrEngineNotStarted before Start, and an error wrapping
// ErrDuplicatePublisherID when a publisher ID is already registered for this
// kind (publisher IDs are unique across tenants) or repeated in the call. On any
// error nothing from the call is registered or started. Engine.Stop closes every
// registered publisher, whichever tenant it was registered for; there is no
// per-tenant unregister.
func (engine *Engine) AddEventPublishersForTenant(id tenancy.TenantID, publishers ...EventPublisher) error {
	if !engine.Started() {
		return ErrEngineNotStarted
	}

	scope, err := engine.tenantPublicationScope(id)
	if err != nil {
		return err
	}

	return engine.registerEventPublishers(scope, publishers)
}

// registerEventPublishers registers publishers on the events stream for scope.
func (engine *Engine) registerEventPublishers(scope persistence.Scope, publishers []EventPublisher) error {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()

	ids := make([]string, len(publishers))
	for i, publisher := range publishers {
		ids[i] = publisher.ID()
	}
	if err := duplicatePublisherIDs(ids, func(id string) bool {
		_, ok := engine.eventsStreams.Get(id)
		return ok
	}); err != nil {
		return err
	}

	for _, publisher := range publishers {
		subscriber := engine.eventStream.AddSubscriber()
		engine.logger.Debug("events publisher subscribing to topic", "publisher", publisher.ID(), "topic", protocol.EventsTopic)
		if err := protocol.SubscribeScoped(engine.eventStream, subscriber, scope, protocol.EventsTopic); err != nil {
			engine.eventStream.RemoveSubscriber(subscriber)
			return err
		}

		// create an instance of the event subscriber
		eventSubscriber := &eventsStream{
			publisher:  publisher,
			subscriber: subscriber,
			done:       make(chan Done, 1),
		}

		// add the event publisher to the engine
		engine.eventsStreams.Set(publisher.ID(), eventSubscriber)

		// start the event publisher
		engine.logger.Info("starting events publisher", "publisher", publisher.ID())
		go engine.sendEvent(eventSubscriber)
	}

	return nil
}

// AddStatePublishers registers one or more state publishers with the Urd engine.
// This function subscribes the publishers to the event stream, allowing them to receive state changes.
//
// Note: State publishers are responsible for publishing durable state changes to external systems.
// They need to be added to the engine before processing any durable state.
//
// Parameters:
//   - publishers: A list of state publishers to be added to the engine.
//
// Returns ErrEngineNotStarted if the engine has not started, and an error
// wrapping ErrDuplicatePublisherID, naming the duplicate IDs, when a
// publisher ID is already registered for this kind or repeated in the call;
// in that case no publisher from the call is registered or started.
func (engine *Engine) AddStatePublishers(publishers ...StatePublisher) error {
	if !engine.Started() {
		return ErrEngineNotStarted
	}

	scope, err := engine.publicationScope()
	if err != nil {
		return err
	}

	return engine.registerStatePublishers(scope, publishers)
}

// AddStatePublishersForTenant is AddStatePublishers for exactly tenant id, in a
// tenant-aware engine whether or not its resolver fixes a tenant (EGO-TENANT-005).
//
// id is validated with tenancy.NewTenantID; an empty or invalid id returns an
// error matching ErrInvalidPublicationTenant. A publisher registered here
// receives only that tenant's publications, in a context for which
// tenancy.From names the tenant; it never receives another tenant's, an
// unscoped one, or one with administrative metadata. There is no wildcard, no
// empty id meaning "all", and no administrative or Unscoped() fallback.
//
// The engine must be tenant-aware, and when its resolver fixes a tenant, id must
// be that tenant: otherwise the call returns ErrPublicationTenantMismatch. It
// returns ErrEngineNotStarted before Start, and an error wrapping
// ErrDuplicatePublisherID when a publisher ID is already registered for this
// kind (publisher IDs are unique across tenants) or repeated in the call. On any
// error nothing from the call is registered or started. Engine.Stop closes every
// registered publisher, whichever tenant it was registered for; there is no
// per-tenant unregister.
func (engine *Engine) AddStatePublishersForTenant(id tenancy.TenantID, publishers ...StatePublisher) error {
	if !engine.Started() {
		return ErrEngineNotStarted
	}

	scope, err := engine.tenantPublicationScope(id)
	if err != nil {
		return err
	}

	return engine.registerStatePublishers(scope, publishers)
}

// registerStatePublishers registers publishers on the events stream for scope.
func (engine *Engine) registerStatePublishers(scope persistence.Scope, publishers []StatePublisher) error {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()

	ids := make([]string, len(publishers))
	for i, publisher := range publishers {
		ids[i] = publisher.ID()
	}
	if err := duplicatePublisherIDs(ids, func(id string) bool {
		_, ok := engine.statesStreams.Get(id)
		return ok
	}); err != nil {
		return err
	}

	for _, publisher := range publishers {
		subscriber := engine.eventStream.AddSubscriber()
		engine.logger.Debug("durable state publisher subscribing to topic", "publisher", publisher.ID(), "topic", protocol.StatesTopic)
		if err := protocol.SubscribeScoped(engine.eventStream, subscriber, scope, protocol.StatesTopic); err != nil {
			engine.eventStream.RemoveSubscriber(subscriber)
			return err
		}

		// create an instance of the state subscriber
		stateSubscriber := &statesStream{
			publisher:  publisher,
			subscriber: subscriber,
			done:       make(chan Done, 1),
		}

		// add the state publisher to the engine
		engine.statesStreams.Set(publisher.ID(), stateSubscriber)

		// start the state publisher
		engine.logger.Info("starting durable state publisher", "publisher", publisher.ID())
		go engine.sendState(stateSubscriber)
	}

	return nil
}

// sendEvent sends events to the event publisher.
// It blocks on the subscriber's Ready signal when idle, then drains the
// snapshot returned by Iterator. Selecting on Iterator directly would
// busy-spin a CPU core: it returns a closed snapshot channel that yields
// nil immediately whenever the queue is empty.
func (engine *Engine) sendEvent(stream *eventsStream) {
	for {
		select {
		case <-stream.done:
			return
		case <-stream.subscriber.Ready():
		}

		for message := range stream.subscriber.Iterator() {
			select {
			case <-stream.done:
				return
			default:
			}

			if message == nil {
				continue
			}

			event, ok := message.Payload().(*egopb.Event)
			if !ok {
				continue
			}

			pubCtx, err := deliveryContext(message.Scope(), event.GetTenantMetadata())
			if err != nil {
				engine.rejectPublication(stream.publisher.ID(), event.GetPersistenceId(), message.Scope(), err)
				continue
			}

			if err := stream.publisher.Publish(pubCtx, event); err != nil {
				engine.logger.Error("failed to publish event",
					"publisher", stream.publisher.ID(),
					"persistence_id", event.GetPersistenceId(),
					"sequence_number", event.GetSequenceNumber(),
					"error", err)
				continue
			}

			engine.logger.Info("event published",
				"publisher", stream.publisher.ID(),
				"persistence_id", event.GetPersistenceId(),
				"sequence_number", event.GetSequenceNumber())
		}
	}
}

// sendState sends state changes to the state publisher.
// It blocks on the subscriber's Ready signal when idle, then drains the
// snapshot returned by Iterator. Selecting on Iterator directly would
// busy-spin a CPU core: it returns a closed snapshot channel that yields
// nil immediately whenever the queue is empty.
func (engine *Engine) sendState(stream *statesStream) {
	for {
		select {
		case <-stream.done:
			return
		case <-stream.subscriber.Ready():
		}

		for message := range stream.subscriber.Iterator() {
			select {
			case <-stream.done:
				return
			default:
			}

			if message == nil {
				continue
			}

			msg, ok := message.Payload().(*egopb.DurableState)
			if !ok {
				continue
			}

			publisher := stream.publisher
			pubCtx, err := deliveryContext(message.Scope(), msg.GetTenantMetadata())
			if err != nil {
				engine.rejectPublication(publisher.ID(), msg.GetPersistenceId(), message.Scope(), err)
				continue
			}

			if err := publisher.Publish(pubCtx, msg); err != nil {
				engine.logger.Error("failed to publish durable state",
					"publisher", publisher.ID(),
					"persistence_id", msg.GetPersistenceId(),
					"version", msg.GetVersionNumber(),
					"error", err)
				continue
			}

			engine.logger.Info("durable state published",
				"publisher", publisher.ID(),
				"persistence_id", msg.GetPersistenceId(),
				"version", msg.GetVersionNumber())
		}
	}
}
