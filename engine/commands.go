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
	"time"

	goakterrors "github.com/tochemey/goakt/v4/errors"
	"go.opentelemetry.io/otel/trace"

	"github.com/getsyntegrity/urd/command"
	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/internal/instrumentation"
	"github.com/getsyntegrity/urd/tenancy"
)

// Dispatch sends env's payload to the entity identified by entityID and
// returns the canonical command.Result (EGO-WRITE-003 adoption, #60). It is
// the primitive SendCommand now adapts to: SendCommand builds an Envelope
// around its bare Command and maps the Result back to the legacy
// (State, uint64, error) shape (resultToLegacy).
//
// env's Metadata crosses the goakt actor boundary as a command.Carrier
// attached to ctx (M-3, design.md option (c)): on a local hop goakt's
// SendSync passes ctx through unchanged, so the receiving actor
// rematerializes the same Metadata via command.UnmarshalMetadata without a
// wire change (command_context.go). This does not yet cover a genuinely
// remote or cluster hop — see the #60 report for that explicitly deferred
// gap.
//
// Dispatch rejects env outright, before any SendSync call, in three cases:
// Metadata that fails the same Marshal/UnmarshalMetadata round-trip the
// receiving actor depends on (a plain error — a caller-side defect, never
// silently downgraded to the HandleCommand fallback), ctx already done
// (canceled or its own deadline expired), and an effective deadline already
// in the past (an OutcomeTimedOut/OutcomeCanceled Result).
//
// The effective deadline is min(ctx's own deadline if any, env.Metadata()'s
// deadline if any, now+timeout) — not merely how long the caller is willing
// to wait for a reply. It is propagated into a derived, cancelable ctx (via
// context.WithDeadline) passed to SendSync, so the target actor can itself
// detect and fail closed on it — both before invoking the handler and again
// before persisting anything — since context.WithDeadline alone does not
// preempt a handler that ignores its context. See checkDeadline
// (deadline_gate.go) and its call sites in EventSourcedActor/
// DurableStateActor for the actor-side half of this.
func (engine *Engine) Dispatch(ctx context.Context, entityID string, env command.Envelope, timeout time.Duration) (result command.Result, err error) {
	if !engine.Started() {
		return command.Result{}, ErrEngineNotStarted
	}

	// entityID is not defined
	if entityID == "" {
		return command.Result{}, ErrUndefinedEntityID
	}

	if _, internal := env.Payload().(*egopb.TenantBindingQuery); internal {
		return command.Result{}, ErrNotACommand
	}

	// env may be a caller-constructed zero-value command.Envelope{} (Envelope's
	// fields are unexported, but Go allows an empty struct literal from any
	// package). NewEnvelope rejects a nil payload, but that guard is bypassed
	// entirely here, so Payload() can be nil. Reject it before the telemetry
	// span below dereferences it via ProtoReflect(), which panics on a nil
	// proto.Message interface.
	if env.Payload() == nil {
		return command.Result{}, command.ErrInvalidEnvelope
	}

	// Create a trace span that connects the caller's context (e.g. an HTTP
	// request span) to the command dispatch, providing end-to-end visibility.
	if engine.telemetry != nil && engine.telemetry.Tracer != nil {
		var span trace.Span
		ctx, span = instrumentation.StartSendCommandSpan(ctx, engine.telemetry.Tracer, entityID, env.Payload())
		defer func() { instrumentation.EndSendCommandSpan(span, err) }()
	}

	ref := engine.actorSystem.Load()
	if ref == nil {
		return command.Result{}, ErrEngineNotStarted
	}

	// env's Metadata must round-trip through the same
	// Marshal/UnmarshalMetadata pair the receiving actor uses to
	// rematerialize it (command_context.go). Skipping this check lets an
	// invalid Metadata (e.g. a zero-value command.Metadata{} slipped into
	// NewEnvelope, which does not validate md) reach SendSync unchecked:
	// the actor's UnmarshalMetadata then fails silently and
	// dispatchToBehavior falls back to HandleCommand, running the payload
	// as a legacy command instead of surfacing the caller's error.
	if _, unmarshalErr := command.UnmarshalMetadata(command.MarshalMetadata(env.Metadata())); unmarshalErr != nil {
		return command.Result{}, unmarshalErr
	}

	// ctx may already be done — canceled by the caller, or past its own
	// deadline — before Dispatch even starts. Reject outright rather than
	// let SendSync discover it a moment later after paying for tenant
	// resolution and carrier attachment.
	if ctxErr := ctx.Err(); ctxErr != nil {
		failure, failureErr := command.NewFailure("command: context already done before dispatch", command.WithFailureCause(ctxErr))
		if failureErr != nil {
			return command.Result{}, failureErr
		}
		if errors.Is(ctxErr, context.Canceled) {
			return command.NewCanceled(env.Metadata(), failure)
		}
		return command.NewTimedOut(env.Metadata(), failure)
	}

	// Effective deadline = min(ctx's own deadline if any, env.Metadata()'s
	// deadline if any, now+timeout). This bounds when the handler is
	// allowed to run and persist, not merely how long the caller is willing
	// to wait for a reply.
	deadline := time.Now().Add(timeout)
	if mdDeadline, ok := env.Metadata().Deadline(); ok && mdDeadline.Before(deadline) {
		deadline = mdDeadline
	}
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if !deadline.After(time.Now()) {
		failure, failureErr := command.NewFailure("command: deadline already exceeded")
		if failureErr != nil {
			return command.Result{}, failureErr
		}
		return command.NewTimedOut(env.Metadata(), failure)
	}

	// Derive a cancelable, deadline-bound ctx and propagate it (not just a
	// shrunk timeout duration) all the way to SendSync and, through it, to
	// the target actor's goCtx — this is what makes the actor-side
	// checkDeadline gates possible.
	var cancel context.CancelFunc
	ctx, cancel = context.WithDeadline(ctx, deadline)
	defer cancel()
	timeout = time.Until(deadline)

	// Tenant-aware mode: resolve the caller's tenant identity exactly once,
	// here, at the single trust boundary between external callers and the
	// actor runtime, and attach it to ctx before it ever reaches dispatch.
	// A resolver error blocks the command outright: no dispatch, no actor,
	// no handler, no persistence. Legacy mode (no resolver registered) is
	// byte-identical: this whole block is skipped and ctx is untouched.
	actorName := entityID
	if engine.tenantResolver != nil {
		tenantContext, resolveErr := engine.tenantResolver.Resolve(ctx)
		if resolveErr != nil {
			return command.Result{}, resolveErr
		}

		attachedCtx, attachErr := tenancy.Attach(ctx, tenantContext)
		if attachErr != nil {
			return command.Result{}, attachErr
		}
		ctx = attachedCtx

		// The entity is addressed by (tenant, ID): the command reaches the
		// actor of the caller's own tenant. An ID that only another tenant
		// has spawned is not found here; it is never that tenant's actor.
		var nameErr error
		if actorName, nameErr = engine.actorNameFor(tenantContext, entityID); nameErr != nil {
			return command.Result{}, nameErr
		}
	}

	ctx = protocol.AttachCarrier(ctx, command.MarshalMetadata(env.Metadata()))

	// Every call is its own request to the entity, even when two calls send the
	// same envelope: the actor tells them apart by this token.
	ctx = protocol.AttachRequestToken(ctx)

	reply, sendErr := ref.noSender.SendSync(ctx, actorName, env.Payload(), timeout)
	if sendErr != nil {
		// SendSync/goakt's Ask races ctx.Done() against its own internal
		// timer derived from the timeout argument. When ctx.Done() wins it
		// returns errors.Join(ctx.Err(), goakterrors.ErrRequestTimeout); when
		// the internal timer wins instead it returns the bare, unwrapped
		// goakterrors.ErrRequestTimeout — neither ctx.Canceled nor
		// ctx.DeadlineExceeded. Since Dispatch aligns ctx's deadline and
		// timeout to the same instant, either race outcome is possible, so
		// both must classify as a Result (Timed/CanceledOut carries the same
		// contract as the pre-dispatch checks above) rather than a bare
		// plumbing error, since it is exactly the deadline/cancellation the
		// command package models.
		if errors.Is(sendErr, context.Canceled) {
			failure, failureErr := command.NewFailure("command: caller context canceled while waiting for reply", command.WithFailureCause(sendErr))
			if failureErr != nil {
				return command.Result{}, failureErr
			}
			return command.NewCanceled(env.Metadata(), failure)
		}
		if errors.Is(sendErr, context.DeadlineExceeded) || errors.Is(sendErr, goakterrors.ErrRequestTimeout) {
			failure, failureErr := command.NewFailure("command: deadline exceeded while waiting for reply", command.WithFailureCause(sendErr))
			if failureErr != nil {
				return command.Result{}, failureErr
			}
			return command.NewTimedOut(env.Metadata(), failure)
		}
		return command.Result{}, sendErr
	}

	// cast the reply as it supposes
	commandReply, ok := reply.(*egopb.CommandReply)
	if !ok {
		return command.Result{}, ErrCommandReplyUnmarshalling
	}

	return protocol.ResultFromReply(commandReply, env.Metadata())
}

// SendCommand sends a command to the specified entity and processes its response.
//
// This function dispatches a command to an entity identified by `entityID`. The entity validates the command, applies
// any necessary state changes, and persists the resulting state if applicable. The function returns the updated state,
// a revision number, or an error if the operation fails.
//
// Behavior:
//   - If the command is successfully processed and results in a state update, the new state and its revision number are returned.
//   - If no state update occurs (i.e., no event is persisted or the command does not trigger a change), `nil` is returned.
//   - If an error occurs during processing, the function returns a non-nil error.
//
// Parameters:
//   - ctx: Execution context for handling timeouts and cancellations.
//   - entityID: The unique identifier of the target entity.
//   - cmd: The command to be processed by the entity.
//   - timeout: The duration within which the command must be processed before timing out.
//
// Returns:
//   - resultingState: The updated state of the entity after handling the command, or `nil` if no state change occurred.
//   - revision: A monotonically increasing revision number representing the persisted state version.
//   - err: An error if the command processing fails.
//
// SendCommand is a thin adapter over Dispatch (EGO-WRITE-003 adoption,
// #60): it wraps cmd in a command.Envelope carrying freshly derived
// Metadata and maps the resulting command.Result back to this legacy
// shape. It remains fully supported; see design.md's Migration/Rollout
// section for the deprecation timeline (not before Dispatch et al. have
// run in production for a release, removal only in /v5).
func (engine *Engine) SendCommand(ctx context.Context, entityID string, cmd Command, timeout time.Duration) (resultingState State, revision uint64, err error) {
	md, err := engine.deriveMetadata(ctx)
	if err != nil {
		return nil, 0, err
	}

	env, err := command.NewEnvelope(cmd, md)
	if err != nil {
		return nil, 0, err
	}

	result, err := engine.Dispatch(ctx, entityID, env, timeout)
	if err != nil {
		return nil, 0, err
	}

	return resultToLegacy(result)
}

// deriveMetadata builds a SendCommand call's Metadata: a child derived
// (Metadata.Derive, D7) from a Carrier already attached to ctx — e.g. a
// behavior's HandleCommand that itself calls SendCommand while already
// inside a Dispatch — so nested legacy call sites get a causally-chained
// operation for free, or a fresh root Metadata otherwise.
//
// It never touches ctx's tenancy attachment: Metadata's own tenant slot is
// intentionally left unset here. Populating it is tenant+aggregate identity
// propagation into command.Metadata, which is #54's scope, not #60's.
func (engine *Engine) deriveMetadata(ctx context.Context) (command.Metadata, error) {
	op, err := command.GenerateOperationID()
	if err != nil {
		return command.Metadata{}, err
	}
	if parent, ok := protocol.MetadataFromContext(ctx); ok {
		return parent.Derive(op)
	}
	return command.NewMetadata(op)
}

// resultToLegacy maps a command.Result back onto SendCommand's legacy
// (State, uint64, error) shape: OutcomeSuccess unpacks to (state, revision,
// nil), OutcomeSuccessNoState unpacks to (nil, 0, nil) exactly like the
// original zero-events branch of SendCommand did, and every other outcome
// (Rejected, Failed, TimedOut, Canceled) unpacks to (nil, 0, result.Err()).
// result.Err() classifies via errors.Is against command.ErrRejected et al.,
// but its Error() string is byte-identical to what the pre-#60 SendCommand
// returned for the same reply (a plain errors.New(message)), so no existing
// caller comparing error strings observes a behavior change.
func resultToLegacy(result command.Result) (State, uint64, error) {
	switch result.Outcome() {
	case command.OutcomeSuccess:
		state, _ := result.State()
		return state, result.Revision(), nil
	case command.OutcomeSuccessNoState:
		return nil, 0, nil
	default:
		return nil, 0, result.Err()
	}
}
