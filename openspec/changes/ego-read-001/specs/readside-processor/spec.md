# Read-Side Processor Specification (PROPOSED)

## Purpose

Define, from behaviour that already exists in `projection/`,
`engine/projections.go` and `internal/projectionrunner`, what a read-side
processor is. Status: proposal for owner review; not implemented as a named
API.

## Requirements

### Requirement: A processor is identified by a name and a scope

A processor MUST have a non-empty name, unique within its scope on one
engine. Its effective identity MUST be the pair (scope, name), where scope is
`persistence.Unscoped()` or a tenant scope. Registering the same pair twice
MUST keep the existing last-wins behaviour of `WithProjection`.

#### Scenario: Same name, different scope

- GIVEN a processor named "balances" registered for tenant A and another
  named "balances" registered for tenant B
- WHEN both are started
- THEN they are two processors with independent identity

#### Scenario: Unscoped registration

- GIVEN a processor registered with no scope declared
- WHEN it is started
- THEN its scope is `persistence.Unscoped()` and behaviour matches today's

### Requirement: Scope is explicit and never inferred

The scope MUST be declared at registration or start. It MUST NOT be taken from
event payload or `tenant_metadata`, and MUST NOT be built by prefixing or
parsing the name. A zero-value scope MUST be rejected.

#### Scenario: Zero scope rejected

- GIVEN a registration whose scope is the zero `persistence.Scope`
- WHEN it is registered or started
- THEN it fails with an error and no processing begins

### Requirement: Consumption is durable, ordered and at-least-once

A processor MUST resume from its last committed position after a restart.
Events of one persistence ID MUST reach the handler in revision order.
Delivery MUST be at-least-once; handlers MUST tolerate redelivery.

#### Scenario: Restart resumes

- GIVEN a processor that committed a position and then stopped
- WHEN it is started again
- THEN it continues from the committed position, not from the beginning

#### Scenario: Crash mid-batch redelivers

- GIVEN a batch whose position was not yet committed when the processor died
- WHEN the processor restarts
- THEN the whole batch is delivered again

### Requirement: Handler failure follows the declared recovery policy

A handler error or panic MUST follow the processor's recovery policy
(fail, retry then fail, retry then skip). Skipped events MUST go to the
dead-letter handler when one is set. A failed store round trip MUST be
retried in place and MUST NOT stop the processor.

### Requirement: Lifecycle

A processor MUST support register, start, stop, running-state query and
rebuild from a timestamp. Starting an unregistered processor MUST fail with
`ErrProjectionNotRegistered`. In cluster mode every node MUST register the
same processors, and the processor MUST run once cluster-wide.

### Requirement: Out of scope

This contract MUST NOT define the offset store, claiming, leases, fencing,
partitioning, failover, adapters, the canonical event envelope, or topic
isolation.
