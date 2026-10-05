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

package saga

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	kitlog "github.com/pablogore/kit-logger/pkg/logger"
	goakt "github.com/tochemey/goakt/v4/actor"
	"github.com/tochemey/goakt/v4/extension"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/getsyntegrity/urd/command"
	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/eventstream"
	"github.com/getsyntegrity/urd/internal/actoridentity"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/internal/extensions"
	"github.com/getsyntegrity/urd/internal/goaktlog"
	"github.com/getsyntegrity/urd/persistence"
	behaviorport "github.com/getsyntegrity/urd/port/behavior"
	runtimeport "github.com/getsyntegrity/urd/port/runtime"
	"github.com/getsyntegrity/urd/tenancy"
)

// sagaTimeoutMsg is an internal message sent when the saga timeout expires.
type sagaTimeoutMsg struct{}

// Actor implements a saga/process manager as a Go-Akt actor.
// It subscribes to the event stream, reacts to events via the SagaBehavior,
// persists its own events, and coordinates commands to other entities.
type Actor struct {
	behavior      behaviorport.Saga
	eventsStore   persistence.EventsStore
	eventsStream  eventstream.Stream
	subscriber    eventstream.Subscriber
	currentState  State
	eventsCounter uint64
	status        runtimeport.SagaStatus
	sagaID        string
	// qualifiedNames is true when the engine addresses actors by (tenant,
	// ID): the saga then reaches the entities it commands in its own tenant.
	qualifiedNames bool
	timeout        time.Duration

	// tenantAware records whether this saga instance runs under a tenancy
	// resolver (presence-only signal via extensions.TenancyExtensionID, set
	// once in PreStart before recover() runs). A no-op sentinel in legacy
	// mode: every tenant gate below short-circuits when this is false, so
	// legacy behavior is byte-identical (EGO-TENANT-002 PR3, SG4).
	tenantAware bool
	// boundTenant is the tenant this saga instance is bound to for its
	// entire lifetime, seeded from the first valid tenant observed on a
	// stream event (live, SG4) or the first replayed saga event (recovery,
	// SG5), mirroring EventSourcedActor.actorTenant/DurableStateActor's
	// equivalent. Every later event is cross-checked against it via
	// tenancy.VerifyUnchanged; a mismatch is rejected fail-closed. Cross-
	// tenant sagas are explicitly out of scope (deferred). Zero value is
	// noTenantContext (declared in event_sourced_actor.go) until seeded.
	boundTenant tenancy.TenantContext

	// scope is the persistence.Scope this saga's own event store reads and
	// writes are bound to (TENANT-003 T4), mirroring EventSourcedActor.scope
	// position-for-position. Bound once in PreStart via resolveScope, right
	// after tenantAware is set and BEFORE any store read (including
	// recover()): persistence.Unscoped() when tenantAware is false, or the
	// tenant scope carried by the per-spawn extensions.EntityTenantScope
	// dependency Engine.Saga injects when tenantAware is true. Binding also
	// pre-seeds boundTenant with the spawn-bound tenancy.TenantContext,
	// turning recover()'s replay-path binding (SG5, bindOrVerify) into a
	// cross-check against the spawn-bound tenant instead of a first seed.
	// The live stream path (handleStreamEvent, SG4) is unaffected in shape:
	// since boundTenant is already non-zero once PreStart returns, its
	// "already bound" branch always applies for a tenant-aware saga, and
	// bindOrVerify cross-checks every event exactly as it already did once
	// bound — the SG-DUR1 durable-ownership-marker path (persistTenantBinding)
	// simply never fires anymore for a tenant-aware saga, since spawn itself
	// is now the durable source of truth for which tenant owns this sagaID.
	//
	// # Actor identity across tenants
	//
	// In a multi-tenant engine the actor's name is qualified with its tenant
	// (actoridentity.Qualify), so two tenants that share an sagaID are two
	// actors with two scopes and two actorTenant values; neither can reach or
	// lock out the other (EGO-TENANT-009). PreStart checks that its name is
	// the one its tenant and ID derive before it reads a store, and the
	// persistence ID stays the sagaID the behavior declares. An engine with
	// exactly one tenant, or none, keeps the bare sagaID as the actor's
	// name, where a spawn under another tenant is still ErrSpawnTenantMismatch
	// (engine's verifySpawnedTenant) and the actorTenant cross-check stays as
	// an additional defense.
	scope persistence.Scope

	// rootMetadata is this saga instance's own root command.Metadata (#60,
	// M-3), established once in PreStart from sagaID. Every command the
	// saga dispatches (sendCommand, compensate) derives a fresh child from
	// it via attachCommandMetadata, so the receiving actor can
	// rematerialize a causally-chained Metadata exactly as it would for a
	// command reached through Engine.Dispatch/SendCommand.
	rootMetadata command.Metadata

	// actorSystem, logger and self are stored during PostStart so that the
	// consumeEvents goroutine can use them safely after the ReceiveContext
	// from PostStart has been returned to the pool.
	actorSystem goakt.ActorSystem
	logger      kitlog.Logger
	self        *goakt.PID

	// stop ends the event consumption loop of the current incarnation. PreStart
	// makes a new one each time the actor starts, because GoAkt builds a
	// relocated actor from its registered type (the zero value, not New) and
	// reuses the same value after a restart. PostStop only reads the field, so
	// it cannot race with Receive, which reads it when it starts the loop; only
	// PreStart writes it, before the incarnation processes its first message.
	stop *stopSignal
}

// stopSignal ends the event consumption loop of one incarnation of the actor.
// Closing it more than once is safe: a stopped actor that stops again, as when
// a start fails after PreStart began, must not panic.
type stopSignal struct {
	once sync.Once
	ch   chan struct{}
}

func newStopSignal() *stopSignal {
	return &stopSignal{ch: make(chan struct{})}
}

func (x *stopSignal) close() {
	x.once.Do(func() { close(x.ch) })
}

// implements the goakt.Actor interface
var _ goakt.Actor = (*Actor)(nil)

// New creates a new saga actor.
// No arguments are passed in the constructor to support cluster relocation.
// The SagaBehavior and timeout are passed via dependencies.
func New() *Actor {
	return &Actor{}
}

// PreStart initializes the saga actor: loads stores, recovers state, subscribes to events.
func (s *Actor) PreStart(ctx *goakt.Context) error {
	// One stop signal per incarnation (see stop).
	s.stop = newStopSignal()
	eventsStoreExt, err := extensions.Require[*extensions.EventsStore](ctx, extensions.EventsStoreExtensionID)
	if err != nil {
		return err
	}
	eventsStreamExt, err := extensions.Require[*extensions.EventsStream](ctx, extensions.EventsStreamExtensionID)
	if err != nil {
		return err
	}
	s.eventsStore = eventsStoreExt.Underlying()
	s.eventsStream = eventsStreamExt.Underlying()
	s.sagaID = ctx.ActorName()
	// Presence-only signal, set before recover() so replay validation (SG5)
	// gates on the same tenantAware value the live path uses (SG4).
	s.tenantAware = ctx.Extension(extensions.TenancyExtensionID) != nil
	s.qualifiedNames = extensions.QualifiedActorNames(ctx)

	if err := s.resolveScope(ctx.Dependencies()); err != nil {
		return err
	}

	for _, dependency := range ctx.Dependencies() {
		if dependency == nil {
			continue
		}

		if behavior, ok := extensions.BehaviorFrom[behaviorport.Saga](dependency); ok {
			s.behavior = behavior
		}

		if cfg, ok := dependency.(*extensions.SagaConfig); ok {
			s.timeout = cfg.Timeout
		}
	}

	if s.behavior == nil {
		return fmt.Errorf("saga behavior is required")
	}

	// The actor's name is only its address: in a multi-tenant engine it is
	// qualified with the tenant, and the saga ID the records and the root
	// metadata carry stays the ID the behavior declares (EGO-TENANT-009).
	// Before any store read, prove the name is the one this saga's tenant and
	// ID derive.
	s.sagaID = s.behavior.ID()
	if s.tenantAware {
		if err := extensions.VerifyActorIdentity(ctx, string(s.scope.TenantID()), s.sagaID); err != nil {
			return err
		}
	}

	rootOp, err := command.NewOperationID(s.sagaID)
	if err != nil {
		return fmt.Errorf("saga: invalid saga id for root command metadata: %w", err)
	}
	s.rootMetadata, err = command.NewMetadata(rootOp)
	if err != nil {
		return fmt.Errorf("saga: failed to build root command metadata: %w", err)
	}

	if err := s.eventsStore.Ping(ctx.Context()); err != nil {
		return fmt.Errorf("saga events store ping failed: %w", err)
	}

	if err := s.recover(ctx.Context()); err != nil {
		return err
	}

	// Subscribe to the single in-process events topic. Sagas observe events
	// from every shard; the shard is carried in the event payload for any
	// downstream filtering the saga behavior wants to apply.
	s.subscriber = s.eventsStream.AddSubscriber()
	// EGO-TENANT-005: the saga subscribes for the scope it is bound to, so a
	// tenant's saga never sees another tenant's events.
	if err := protocol.SubscribeScoped(s.eventsStream, s.subscriber, s.scope, protocol.EventsTopic); err != nil {
		s.eventsStream.RemoveSubscriber(s.subscriber)
		return err
	}

	return nil
}

// Receive handles messages sent to the saga actor.
//
// All saga state (currentState, eventsCounter, status) is read and written
// exclusively from here: the consumeEvents goroutine forwards stream events
// to the actor's own mailbox instead of processing them itself, so the
// actor's serialized message loop is the only writer.
func (s *Actor) Receive(ctx *goakt.ReceiveContext) {
	switch message := ctx.Message().(type) {
	case *goakt.PostStart:
		// Capture stable references before the ReceiveContext is returned to the pool.
		s.actorSystem = ctx.ActorSystem()
		s.logger = goaktlog.Backend(ctx.Logger())
		s.self = ctx.Self()
		// Start consuming events from the stream
		go s.consumeEvents(s.stop.ch, s.subscriber)
		// Schedule timeout if configured
		if s.timeout > 0 {
			self := ctx.Self()
			_ = ctx.ActorSystem().ScheduleOnce(ctx.Context(), &sagaTimeoutMsg{}, self, s.timeout)
		}
	case *egopb.Event:
		s.handleStreamEvent(message)
	case *sagaTimeoutMsg:
		if s.status == runtimeport.SagaRunning {
			s.status = runtimeport.SagaCompensating
			// The timer fires independently of any event delivery, so there
			// is no live per-event ctx to reuse here; reconstruct one from
			// boundTenant. Fails closed (via tenancy.Attach's own zero-value
			// rejection) when the saga never bound a tenant before timing
			// out (SG4).
			sagaCtx, err := s.compensationContext()
			if err != nil {
				s.logger.Error("saga: timeout compensation aborted, no bound tenant", "saga_id", s.sagaID, "error", err)
				s.status = runtimeport.SagaFailed
				return
			}
			s.compensate(sagaCtx, s.logger, ctx.ActorSystem())
		}
	case *egopb.GetStateCommand:
		s.getStateAndReply(ctx)
	case *egopb.TenantBindingQuery:
		ctx.Response(protocol.AnswerTenantBinding(s.tenantAware, s.scope, message))
	default:
		ctx.Unhandled()
	}
}

// PostStop cleans up the saga actor.
func (s *Actor) PostStop(_ *goakt.Context) error {
	if s.stop != nil {
		s.stop.close()
	}
	if s.subscriber != nil {
		s.subscriber.Shutdown()
	}
	return nil
}

// resolveScope binds s.scope (and, in tenant-aware mode, s.boundTenant) from
// deps before any store read (TENANT-003 T4), mirroring
// EventSourcedActor.resolveScope/DurableStateActor.resolveScope.
//
// tenantAware == false binds persistence.Unscoped() and leaves boundTenant
// untouched (noTenantContext) — legacy mode is byte-identical to before
// this field existed.
//
// tenantAware == true looks for the per-spawn extensions.EntityTenantScope
// dependency Engine.Saga injects and fails closed with
// extensions.ErrEntityTenantScopeMissing when it is absent or carries an invalid
// tenant id. On success it also pre-seeds s.boundTenant with the
// corresponding tenancy.TenantContext, BEFORE recover() runs, so replayed
// saga events (SG5) are cross-checked against the spawn-bound tenant via
// bindOrVerify instead of seeding it from the first replayed event.
func (s *Actor) resolveScope(deps []extension.Dependency) error {
	if !s.tenantAware {
		s.scope = persistence.Unscoped()
		return nil
	}

	for _, dependency := range deps {
		dep, ok := dependency.(*extensions.EntityTenantScope)
		if !ok || dep == nil {
			continue
		}

		scope, err := persistence.NewTenantScope(tenancy.TenantID(dep.TenantID))
		if err != nil {
			return fmt.Errorf("%w: %w", extensions.ErrEntityTenantScopeMissing, err)
		}

		tenantContext, err := tenancy.NewTenantContext(scope.TenantID())
		if err != nil {
			return fmt.Errorf("%w: %w", extensions.ErrEntityTenantScopeMissing, err)
		}

		s.scope = scope
		s.boundTenant = tenantContext
		return nil
	}

	return extensions.ErrEntityTenantScopeMissing
}

// recover rebuilds the saga state from persisted events.
func (s *Actor) recover(ctx context.Context) error {
	s.currentState = s.behavior.InitialState()
	s.status = runtimeport.SagaRunning

	latestEvent, err := s.eventsStore.GetLatestEvent(ctx, s.scope, s.sagaID)
	if err != nil {
		return fmt.Errorf("failed to get latest saga event: %w", err)
	}

	if latestEvent == nil {
		return nil
	}

	latestSeqNr := latestEvent.GetSequenceNumber()
	events, err := s.eventsStore.ReplayEvents(ctx, s.scope, s.sagaID, 1, latestSeqNr, latestSeqNr)
	if err != nil {
		return fmt.Errorf("failed to replay saga events: %w", err)
	}

	for _, envelope := range events {
		// SG5: validate every replayed event against the tenant seeded by
		// the FIRST replayed event, not last-wins. Absent/undecodable
		// metadata fails closed (ErrInvalid, no backfill); a later event
		// disagreeing with the first fails closed (ErrDenied) — either way
		// PreStart fails and this saga never comes up, mirroring
		// applyPersistedEvent's replay-path gate in event_sourced_actor.go.
		eventCtx, err := s.eventContext(ctx, envelope)
		if err != nil {
			return fmt.Errorf("failed to reconstruct tenant context for saga event at sequence %d: %w", envelope.GetSequenceNumber(), err)
		}

		if err := s.bindOrVerify(eventCtx); err != nil {
			return fmt.Errorf("tenant mismatch replaying saga event at sequence %d: %w", envelope.GetSequenceNumber(), err)
		}

		eventMsg, err := envelope.GetEvent().UnmarshalNew()
		if err != nil {
			return fmt.Errorf("failed to unmarshal saga event at sequence %d: %w", envelope.GetSequenceNumber(), err)
		}

		// A tenant-binding marker (persistTenantBinding, SG-DUR1) carries no
		// business payload — it exists solely to give a first action with no
		// Events of its own a durable record to recover boundTenant from — so
		// bindOrVerify above already did this envelope's only job. Calling
		// ApplyEvent for it would hand the behavior a payload type it never
		// emitted and never expects.
		if _, isBindingMarker := eventMsg.(*emptypb.Empty); isBindingMarker {
			continue
		}

		s.currentState, err = s.behavior.ApplyEvent(eventCtx, eventMsg, s.currentState)
		if err != nil {
			return fmt.Errorf("failed to apply saga event at sequence %d: %w", envelope.GetSequenceNumber(), err)
		}
	}

	s.eventsCounter = latestSeqNr
	return nil
}

// consumeEvents pumps events from the stream into the actor's own mailbox.
// It uses the stable actorSystem, logger and self fields captured during
// PostStart instead of the ReceiveContext (which is returned to the pool once
// Receive returns).
//
// It blocks on the subscriber's Ready signal when idle, then drains the
// snapshot returned by Iterator. Selecting on Iterator directly would
// busy-spin a CPU core: it returns a closed snapshot channel that yields
// nil immediately whenever the queue is empty.
//
// Events are forwarded to the mailbox rather than processed here so that all
// saga state stays owned by the actor's serialized message loop — processing
// them on this goroutine would race with Receive (state queries, timeout).
//
// stop and subscriber belong to the incarnation of the actor that started this
// goroutine, and it reads nothing else that PreStart replaces. A restart makes
// the next incarnation a new stop signal and a new subscriber on the same Actor
// value while this goroutine may still be running; reading either from the
// actor would let it end the new incarnation's loop, or take events from its
// subscriber and drop them.
func (s *Actor) consumeEvents(stop <-chan struct{}, subscriber eventstream.Subscriber) {
	for {
		select {
		case <-stop:
			return
		case <-subscriber.Ready():
		}

		for message := range subscriber.Iterator() {
			select {
			case <-stop:
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

			// Skip our own saga events
			if event.GetPersistenceId() == s.sagaID {
				continue
			}

			if err := s.actorSystem.NoSender().Tell(context.Background(), s.self, event); err != nil {
				s.logger.Error("saga: failed to forward event to mailbox", "saga_id", s.sagaID, "error", err)
			}
		}
	}
}

// eventContext reconstructs the TenantContext carried by event's
// TenantMetadata and attaches it to parent, giving handleStreamEvent (and
// recover, during replay) a context.Context to hand to behavior methods and
// downstream dispatch. parent is always an un-attached context —
// context.Background() in handleStreamEvent, PreStart's ctx in recover —
// never a previously attached one, since sagas cross an in-process
// Tell/mailbox boundary where context.Context isn't preserved: tenant
// identity must travel as data instead (SG1, SG2).
//
// Binding s.boundTenant and cross-checking it against a previously bound
// value happens in the caller (bindOrVerify, SG4/SG5), not here, so that an
// absent/malformed metadata (ErrInvalid) and a foreign but well-formed
// tenant (ErrDenied) remain separately unit-assertable by cause. A no-op in
// legacy mode: parent is returned unchanged.
func (s *Actor) eventContext(parent context.Context, event *egopb.Event) (context.Context, error) {
	if !s.tenantAware {
		return parent, nil
	}

	tc, err := tenancy.UnmarshalMetadata(tenancy.Metadata(event.GetTenantMetadata()))
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal saga event tenant metadata: %w", err)
	}

	return tenancy.Attach(parent, tc)
}

// bindOrVerify enforces SG4 (live stream events, from handleStreamEvent) and
// SG5 (replay, from recover): the saga instance binds to the first valid
// tenant it observes via ctx (already attached by eventContext) and every
// subsequent event is cross-checked against that bound tenant via
// tenancy.VerifyUnchanged, fail-closed on mismatch. A no-op in legacy mode.
func (s *Actor) bindOrVerify(ctx context.Context) error {
	if !s.tenantAware {
		return nil
	}

	tc, err := tenancy.Require(ctx)
	if err != nil {
		return err
	}

	if s.boundTenant == noTenantContext {
		s.boundTenant = tc
		return nil
	}

	return tenancy.VerifyUnchanged(s.boundTenant, tc)
}

// compensationContext reconstructs a context.Context carrying the saga's
// boundTenant for the timeout-triggered compensation path (Receive's
// sagaTimeoutMsg case), which has no live per-event ctx to reuse because the
// timer fires independently of any event delivery. Fails closed via
// tenancy.Attach's own zero-value rejection when the saga has never bound a
// tenant — an unseeded saga timing out before any tenant-bearing event
// arrived (SG4). A no-op in legacy mode.
func (s *Actor) compensationContext() (context.Context, error) {
	if !s.tenantAware {
		return context.Background(), nil
	}
	return tenancy.Attach(context.Background(), s.boundTenant)
}

// handleStreamEvent processes a stream event forwarded by consumeEvents.
// It runs on the actor's message loop, so it can freely touch saga state.
func (s *Actor) handleStreamEvent(event *egopb.Event) {
	if s.status != runtimeport.SagaRunning {
		return
	}

	// eventContext only decodes the event's own tenant metadata onto ctx; it
	// does not touch s.boundTenant.
	ctx, err := s.eventContext(context.Background(), event)
	if err != nil {
		s.logger.Error("saga: rejected stream event, invalid tenant metadata",
			"saga_id", s.sagaID, "persistence_id", event.GetPersistenceId(), "sequence_number", event.GetSequenceNumber(), "error", err)
		return
	}

	// Once bound, verify BEFORE HandleEvent (fail closed without ever
	// exposing a foreign-tenant payload to the saga's own business logic).
	// While unbound, defer the bind past HandleEvent (SG4 correction): every
	// saga on the shared protocol.EventsTopic runs its own entity/type relevance
	// filter first, and only a genuinely actionable result (a non-noop
	// sagaAction — the only signal HandleEvent has for "this belongs to me")
	// commits boundTenant. This stops an unrelated tenant's noise event from
	// poisoning boundTenant before this saga's real initiating event arrives.
	alreadyBound := s.tenantAware && s.boundTenant != noTenantContext
	if alreadyBound {
		if err := s.bindOrVerify(ctx); err != nil {
			s.logger.Error("saga: rejected stream event, tenant mismatch",
				"saga_id", s.sagaID, "persistence_id", event.GetPersistenceId(), "sequence_number", event.GetSequenceNumber(), "error", err)
			return
		}
	}

	eventMsg, err := event.GetEvent().UnmarshalNew()
	if err != nil {
		s.logger.Error("saga: failed to unmarshal event", "saga_id", s.sagaID, "error", err)
		return
	}

	action, err := s.behavior.HandleEvent(ctx, eventMsg, s.currentState)
	if err != nil {
		s.logger.Error("saga: HandleEvent failed", "saga_id", s.sagaID, "error", err)
		return
	}

	if !alreadyBound {
		if actionIsNoop(action) {
			return
		}

		if !s.tenantAware {
			s.processAction(ctx, action)
			return
		}

		tc, err := tenancy.Require(ctx)
		if err != nil {
			s.logger.Error("saga: rejected stream event, tenant mismatch",
				"saga_id", s.sagaID, "persistence_id", event.GetPersistenceId(), "sequence_number", event.GetSequenceNumber(), "error", err)
			return
		}

		// Durably record ownership BEFORE boundTenant is set in memory and
		// before any external effect (command, completion, compensation)
		// runs (SG-DUR1). If action carries its own Events, persisting them
		// (with tenant metadata, SG3) IS that durable record. Otherwise — a
		// perfectly valid action with only Commands, Complete or Compensate —
		// persist a tenant-only binding marker, so a restart can still
		// recover boundTenant and reject a different tenant's later event
		// instead of coming back unbound and letting another tenant claim
		// this sagaID. Either branch returns before boundTenant is ever set
		// if the write fails, so a failed first WriteEvents leaves zero
		// residual appropriation: a retry, same or different tenant, starts
		// clean.
		if len(action.Events) > 0 {
			if err := s.persistAndApplyEvents(ctx, action.Events); err != nil {
				s.logger.Error("saga: failed to persist events", "saga_id", s.sagaID, "error", err)
				return
			}
		} else if err := s.persistTenantBinding(ctx, tc); err != nil {
			s.logger.Error("saga: failed to persist tenant binding", "saga_id", s.sagaID, "error", err)
			return
		}

		s.boundTenant = tc
		s.dispatchActionEffects(ctx, action)
		return
	}

	s.processAction(ctx, action)
}

// processAction executes a sagaAction: persists saga events, sends commands, and handles completion/compensation.
// ctx is the tenant-scoped context established by the caller (handleStreamEvent's
// per-event ctx, or sendCommand's own ctx for result/error-driven follow-up
// actions) and is threaded unchanged into every persist and dispatch below
// (SG1).
func (s *Actor) processAction(ctx context.Context, action *sagaAction) {
	if action == nil {
		return
	}

	// Persist saga events
	if len(action.Events) > 0 {
		if err := s.persistAndApplyEvents(ctx, action.Events); err != nil {
			s.logger.Error("saga: failed to persist events", "saga_id", s.sagaID, "error", err)
			return
		}
	}

	s.dispatchActionEffects(ctx, action)
}

// dispatchActionEffects sends action's commands and applies its
// completion/compensation outcome. Split out from processAction so
// handleStreamEvent's first-bind path (SG-DUR1) can durably persist
// ownership (events or a tenant-binding marker) and set boundTenant itself,
// then dispatch effects here without processAction persisting action.Events
// a second time.
func (s *Actor) dispatchActionEffects(ctx context.Context, action *sagaAction) {
	// Send commands to entities
	for _, cmd := range action.Commands {
		s.sendCommand(ctx, cmd)
	}

	// Handle completion
	if action.Complete {
		s.status = runtimeport.SagaCompleted
		return
	}

	// Handle compensation
	if action.Compensate {
		s.status = runtimeport.SagaCompensating
		s.compensate(ctx, s.logger, s.actorSystem)
	}
}

// persistAndApplyEvents persists saga events and applies them to the saga
// state. In tenant-aware mode, every envelope carries ctx's TenantContext as
// TenantMetadata (SG3), resolved once via tenancy.Require before the loop so
// a missing/invalid tenant on ctx fails closed without persisting or
// mutating anything. A no-op tenant-wise in legacy mode: no metadata is
// written, byte-identical to pre-PR3 behavior.
//
// Envelopes and the resulting state are computed into local variables and
// s.eventsCounter/s.currentState are only assigned after WriteEvents
// succeeds (SG-DUR1, PR#78 review round 2 P1): mutating them first, as a
// prior revision did, left them advanced in memory even when nothing was
// actually persisted, mirroring the exact "mutate before the persist
// boundary" hazard DS1 already ruled unsafe for DurableStateActor.
func (s *Actor) persistAndApplyEvents(ctx context.Context, events []Event) error {
	var tc tenancy.TenantContext
	if s.tenantAware {
		var err error
		tc, err = tenancy.Require(ctx)
		if err != nil {
			return fmt.Errorf("failed to persist saga events: %w", err)
		}
	}

	nextCounter := s.eventsCounter
	nextState := s.currentState
	envelopes := make([]*egopb.Event, 0, len(events))
	for _, event := range events {
		nextCounter++
		eventAny, _ := anypb.New(event)
		envelope := &egopb.Event{
			PersistenceId:  s.sagaID,
			SequenceNumber: nextCounter,
			IsDeleted:      false,
			Event:          eventAny,
			Timestamp:      time.Now().UnixNano(),
		}
		if s.tenantAware {
			envelope.TenantMetadata = tenancy.MarshalMetadata(tc)
		}
		envelopes = append(envelopes, envelope)

		newState, err := s.behavior.ApplyEvent(ctx, event, nextState)
		if err != nil {
			return fmt.Errorf("failed to apply saga event: %w", err)
		}
		nextState = newState
	}

	if err := s.eventsStore.WriteEvents(ctx, s.scope, envelopes, persistence.Unconditional()); err != nil {
		return err
	}

	s.eventsCounter = nextCounter
	s.currentState = nextState
	return nil
}

// persistTenantBinding durably records tc as this saga instance's tenant
// before it is ever set on s.boundTenant, for the first-bind case where the
// triggering action carries no Events of its own to serve as that durable
// record (SG-DUR1, PR#78 review round 2 P1: "a valid action with only
// Commands, Complete or Compensate" — TestTenantWritePathE2E's own saga is
// exactly this shape). The marker carries no business payload
// (behavior.ApplyEvent is deliberately never called for it, see recover's
// *emptypb.Empty check) — other saga instances subscribed to the same
// protocol.EventsTopic see it like any other event type they don't recognize and
// their own HandleEvent relevance filter returns noop for it, the same
// tolerance the design already requires for arbitrary irrelevant domain
// events crossing the shared topic (SG2).
func (s *Actor) persistTenantBinding(ctx context.Context, tc tenancy.TenantContext) error {
	markerAny, err := anypb.New(&emptypb.Empty{})
	if err != nil {
		return fmt.Errorf("failed to marshal saga tenant-binding marker: %w", err)
	}

	nextCounter := s.eventsCounter + 1
	envelope := &egopb.Event{
		PersistenceId:  s.sagaID,
		SequenceNumber: nextCounter,
		IsDeleted:      false,
		Event:          markerAny,
		Timestamp:      time.Now().UnixNano(),
		TenantMetadata: tenancy.MarshalMetadata(tc),
	}

	if err := s.eventsStore.WriteEvents(ctx, s.scope, []*egopb.Event{envelope}, persistence.Unconditional()); err != nil {
		return err
	}

	s.eventsCounter = nextCounter
	return nil
}

// attachCommandMetadata resolves the command.Metadata for one outgoing
// dispatched sagaCommand and attaches it to ctx via a command.Carrier (#60,
// M-3), the same mechanism Engine.Dispatch uses. This is what closes
// sendCommand's/compensate's former bare context.Background() bypass: every
// command a saga sends now carries a causally-chained Metadata to the
// receiving actor, so a behavior implementing
// EventSourcedEnvelopeBehavior/DurableStateEnvelopeBehavior sees it exactly
// as it would for a command reached through Engine.Dispatch/SendCommand,
// instead of always falling back to HandleCommand for saga-originated
// traffic.
//
// explicit is cmd.Metadata: when the behavior set it explicitly (non-zero
// OperationID), it is used as-is, giving the behavior full control over the
// correlation/causation chain for that one command. Otherwise a fresh child
// is derived from s.rootMetadata (established once in PreStart from the
// saga's own id — "la operación disparadora"): correlation inherited,
// causation set to the saga's root operation. A saga has no caller-supplied
// per-request context to propagate — it reacts to events asynchronously off
// its own subscription, not a synchronous caller chain — so, unlike
// Engine.deriveMetadata, there is no "nested call" case to detect here. On
// the rare failure of GenerateOperationID (crypto/rand exhaustion) this
// logs and returns ctx unchanged: the command still dispatches, just
// without Metadata, degrading exactly like any other caller that bypasses
// Engine.Dispatch/SendCommand (see metadataFromContext).
func (s *Actor) attachCommandMetadata(ctx context.Context, explicit command.Metadata) context.Context {
	if explicit.OperationID() != "" {
		return protocol.AttachCarrier(ctx, command.MarshalMetadata(explicit))
	}

	op, err := command.GenerateOperationID()
	if err != nil {
		s.logger.Warn("saga: failed to generate operation id for outgoing command; dispatching without metadata", "saga_id", s.sagaID, "error", err)
		return ctx
	}
	md, err := s.rootMetadata.Derive(op)
	if err != nil {
		s.logger.Warn("saga: failed to derive command metadata; dispatching without metadata", "saga_id", s.sagaID, "error", err)
		return ctx
	}
	return protocol.AttachCarrier(ctx, command.MarshalMetadata(md))
}

// defaultCommandTimeout is used when a saga command leaves Timeout unset.
const defaultCommandTimeout = 5 * time.Second

// commandTimeoutObserver is an unexported test seam: when set, it receives
// the effective timeout of every dispatched command, so tests can prove the
// default without waiting for it. It is nil in production.
var commandTimeoutObserver atomic.Pointer[func(time.Duration)]

// effectiveCommandTimeout resolves the timeout SendSync is given for a
// command and reports it to the test seam, if any.
func effectiveCommandTimeout(configured time.Duration) time.Duration {
	timeout := configured
	if timeout == 0 {
		timeout = defaultCommandTimeout
	}
	if observe := commandTimeoutObserver.Load(); observe != nil {
		(*observe)(timeout)
	}
	return timeout
}

// targetActorName returns the actor name of entityID, an entity of this saga's
// own tenant. In a multi-tenant engine that is the name qualified with the
// saga's tenant, so a saga only ever commands the entities of the tenant it is
// bound to, even when another tenant has an entity with the same ID. Otherwise
// it is entityID itself.
func (s *Actor) targetActorName(entityID string) (string, error) {
	if !s.qualifiedNames {
		return entityID, nil
	}
	return actoridentity.Qualify(string(s.scope.TenantID()), entityID)
}

// sendCommand sends a command to an entity and handles the result. ctx
// carries the saga's tenant identity (attached by the caller) through the
// dispatch to entity B, so the receiving entity's own T4-A gate observes the
// same tenant the saga was bound to (SG1). attachCommandMetadata further
// layers this saga's causally-chained command.Metadata (#60, M-3) on top,
// without erasing the tenant identity ctx already carries.
func (s *Actor) sendCommand(ctx context.Context, cmd sagaCommand) {
	timeout := effectiveCommandTimeout(cmd.Timeout)

	noSender := s.actorSystem.NoSender()
	target, err := s.targetActorName(cmd.EntityID)
	var reply any
	if err == nil {
		reply, err = noSender.SendSync(s.attachCommandMetadata(ctx, cmd.Metadata), target, cmd.Command, timeout)
	}
	if err != nil {
		action, handleErr := s.behavior.HandleError(ctx, cmd.EntityID, err, s.currentState)
		if handleErr != nil {
			s.logger.Error("saga: HandleError failed", "saga_id", s.sagaID, "entity_id", cmd.EntityID, "error", handleErr)
			return
		}
		s.processAction(ctx, action)
		return
	}

	// Parse the reply
	commandReply, ok := reply.(*egopb.CommandReply)
	if !ok {
		s.logger.Error("saga: unexpected reply type from entity", "saga_id", s.sagaID, "entity_id", cmd.EntityID)
		return
	}

	resultState, _, err := protocol.ParseCommandReply(commandReply)
	if err != nil {
		action, handleErr := s.behavior.HandleError(ctx, cmd.EntityID, err, s.currentState)
		if handleErr != nil {
			s.logger.Error("saga: HandleError failed", "saga_id", s.sagaID, "entity_id", cmd.EntityID, "error", handleErr)
			return
		}
		s.processAction(ctx, action)
		return
	}

	action, err := s.behavior.HandleResult(ctx, cmd.EntityID, resultState, s.currentState)
	if err != nil {
		s.logger.Error("saga: HandleResult failed", "saga_id", s.sagaID, "entity_id", cmd.EntityID, "error", err)
		return
	}
	s.processAction(ctx, action)
}

// compensate executes the compensation logic defined by the behavior. ctx is
// supplied by the caller: the live per-event/per-command ctx from
// processAction, or compensationContext()'s reconstruction from boundTenant
// for the timeout path (Receive's sagaTimeoutMsg case).
func (s *Actor) compensate(ctx context.Context, logger kitlog.Logger, actorSystem goakt.ActorSystem) {
	commands, err := s.behavior.Compensate(ctx, s.currentState)
	if err != nil {
		logger.Error("saga: Compensate failed", "saga_id", s.sagaID, "error", err)
		s.status = runtimeport.SagaFailed
		return
	}

	for _, cmd := range commands {
		timeout := effectiveCommandTimeout(cmd.Timeout)

		noSender := actorSystem.NoSender()
		target, err := s.targetActorName(cmd.EntityID)
		if err == nil {
			_, err = noSender.SendSync(s.attachCommandMetadata(ctx, cmd.Metadata), target, cmd.Command, timeout)
		}
		if err != nil {
			logger.Error("saga: compensation command failed", "saga_id", s.sagaID, "entity_id", cmd.EntityID, "error", err)
			s.status = runtimeport.SagaFailed
			return
		}
	}

	s.status = runtimeport.SagaCompleted
}

// replyWithState replies with the saga's current state and lifecycle status.
func (s *Actor) replyWithState(ctx *goakt.ReceiveContext) {
	state, _ := anypb.New(s.currentState)
	reply := &egopb.CommandReply{
		Reply: &egopb.CommandReply_StateReply{
			StateReply: &egopb.StateReply{
				PersistenceId:  s.sagaID,
				State:          state,
				SequenceNumber: s.eventsCounter,
				SagaStatus:     StatusToProto(s.status),
			},
		},
	}
	ctx.Response(reply)
}

// StatusToProto maps a runtimeport.SagaStatus onto the wire enum the saga actor
// reports in its StateReply (#153).
func StatusToProto(status runtimeport.SagaStatus) egopb.SagaLifecycleStatus {
	switch status {
	case runtimeport.SagaCompleted:
		return egopb.SagaLifecycleStatus_SAGA_LIFECYCLE_STATUS_COMPLETED
	case runtimeport.SagaCompensating:
		return egopb.SagaLifecycleStatus_SAGA_LIFECYCLE_STATUS_COMPENSATING
	case runtimeport.SagaFailed:
		return egopb.SagaLifecycleStatus_SAGA_LIFECYCLE_STATUS_FAILED
	default:
		return egopb.SagaLifecycleStatus_SAGA_LIFECYCLE_STATUS_RUNNING
	}
}

// StatusFromProto maps the wire enum of a saga's StateReply back onto a
// runtimeport.SagaStatus (#153). SAGA_LIFECYCLE_STATUS_NONE, which a saga actor built
// before the field existed leaves in its reply, and any value this build does
// not know read as runtimeport.SagaRunning: the status every such reply reported before.
func StatusFromProto(status egopb.SagaLifecycleStatus) runtimeport.SagaStatus {
	switch status {
	case egopb.SagaLifecycleStatus_SAGA_LIFECYCLE_STATUS_COMPLETED:
		return runtimeport.SagaCompleted
	case egopb.SagaLifecycleStatus_SAGA_LIFECYCLE_STATUS_COMPENSATING:
		return runtimeport.SagaCompensating
	case egopb.SagaLifecycleStatus_SAGA_LIFECYCLE_STATUS_FAILED:
		return runtimeport.SagaFailed
	default:
		return runtimeport.SagaRunning
	}
}

// getStateAndReply returns the saga's current state without processing any
// event. Mirrors DurableStateActor.getStateAndReply's gate (the DS4 shape,
// PR#78 review round 2 P1): Receive dispatched *egopb.GetStateCommand
// straight to replyWithState, bypassing handleStreamEvent and bindOrVerify
// entirely, so any caller who knew a sagaID could read another tenant's
// saga state even though the instance is now bound to exactly one tenant
// for its lifetime (SG4).
func (s *Actor) getStateAndReply(ctx *goakt.ReceiveContext) {
	if err := s.checkStateReadTenant(ctx.Context()); err != nil {
		s.sendErrorReply(ctx, err)
		return
	}
	s.replyWithState(ctx)
}

// checkStateReadTenant enforces getStateAndReply's isolation: legacy mode is
// a no-op; tenant-aware mode requires ctx to carry a TenantContext
// (ErrMissing otherwise) and, once this instance is bound, requires it to
// match boundTenant (ErrDenied on mismatch, via VerifyUnchanged). An
// unbound tenant-aware saga has no owner yet to check against, so any
// resolved tenant may read its (still-initial) state — mirrors
// DurableStateActor.getStateAndReply's own unseeded case.
func (s *Actor) checkStateReadTenant(ctx context.Context) error {
	if !s.tenantAware {
		return nil
	}
	tc, err := tenancy.Require(ctx)
	if err != nil {
		return err
	}
	if s.boundTenant != noTenantContext {
		return tenancy.VerifyUnchanged(s.boundTenant, tc)
	}
	return nil
}

// sendErrorReply sends an error as a reply message.
func (s *Actor) sendErrorReply(ctx *goakt.ReceiveContext, err error) {
	ctx.Response(&egopb.CommandReply{
		Reply: &egopb.CommandReply_ErrorReply{
			ErrorReply: &egopb.ErrorReply{
				Message: err.Error(),
			},
		},
	})
}

// noTenantContext is the zero value of tenancy.TenantContext. Neither
// tenancy.NewTenantContext nor tenancy.NewAdministrativeContext can ever
// produce it (tenancy/tenant_context.go), so it safely marks "not yet
// seeded" for an actor's tenant, distinct from any real resolved identity.
var noTenantContext tenancy.TenantContext
