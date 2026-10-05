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
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"strings"

	"github.com/tochemey/goakt/v4/remote"

	"github.com/getsyntegrity/urd/command"
	"github.com/getsyntegrity/urd/internal/engine/protocol"
	"github.com/getsyntegrity/urd/tenancy"
)

// Wire format. GoAkt hands a ContextPropagator an http.Header whose keys it
// canonicalizes and whose values it ships first-value-only, so each piece of
// identity travels as ONE header whose value is a JSON object. Nothing about
// this format leaves this file: the domain sees only tenancy.TenantContext and
// command.Metadata.
const (
	wireHeaderVersion = "Urd-Wire-Version"
	wireHeaderTenant  = "Urd-Tenant"
	wireHeaderCommand = "Urd-Command"

	wireVersion = "1"

	// maxWireValueBytes bounds each header value before it is parsed.
	maxWireValueBytes = 8 << 10

	carrierTenantKeyPrefix = "ego.tenant."
)

// errRemoteIdentityRejected marks every inbound identity this node refuses.
// It crosses the wire only as the text of a GoAkt INVALID_ARGUMENT reply.
var errRemoteIdentityRejected = errors.New("engine: remote tenant identity rejected")

func reject(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errRemoteIdentityRejected, fmt.Sprintf(format, args...))
}

// RemoteOptions returns the GoAkt remoting options a tenant-aware engine
// needs so the caller's tenant identity (and command metadata) survives a hop
// to an actor hosted on another node (#305). Pass them to remote.NewConfig on
// every node of the cluster:
//
//	remote.NewConfig(host, port, cfg.RemoteOptions()...)
//
// It returns nil when no tenant resolver is registered: a legacy engine
// propagates nothing and stays byte-identical.
//
// # Trust boundary
//
// The identity on the wire is a claim made by the sending node. The receiving
// node does not authenticate the sender; that is the transport's job. Run the
// cluster with mutual TLS (goakt remote.WithTLS) or on a network you trust: a
// cluster without mTLS trusts every peer that can reach its remoting port, and
// nothing here claims more than that. The receiving node still enforces:
//   - an absent identity attaches nothing, so the actor's tenancy.Require
//     rejects the command;
//   - a malformed, oversized, duplicated, unknown-version or conflicting
//     identity fails the call (fail closed);
//   - an administrative scope is always rejected on the wire, and never sent;
//   - with a fixed single-tenant resolver only that tenant is accepted;
//   - the actor compares the identity with the one it is bound to
//     (tenancy.VerifyUnchanged), so a tenant that names another tenant's
//     actor is denied. An actor name alone is never authorization.
func (c *Config) RemoteOptions() []remote.Option {
	if c == nil || isNilResolver(c.tenantResolver) {
		return nil
	}
	fixed, _ := tenancy.FixedTenantOf(c.tenantResolver)
	return []remote.Option{remote.WithContextPropagator(tenantPropagator{fixed: fixed})}
}

// tenantPropagator implements remote.ContextPropagator. It is stateless.
type tenantPropagator struct {
	// fixed is the single tenant of a FixedTenantResolver, or empty.
	fixed tenancy.TenantID
}

var _ remote.ContextPropagator = tenantPropagator{}

// Inject writes the tenant identity and command carrier of ctx, if any.
func (tenantPropagator) Inject(ctx context.Context, headers nethttp.Header) error {
	tc, hasTenant := tenancy.From(ctx)
	if hasTenant {
		if tc.Scope() != tenancy.ScopeTenant {
			return reject("administrative scope is never propagated")
		}
		raw, err := json.Marshal(tenancy.MarshalMetadata(tc))
		if err != nil {
			return err
		}
		headers.Set(wireHeaderTenant, string(raw))
	}
	if carrier, ok := protocol.CarrierFromContext(ctx); ok {
		out := make(map[string]string, len(carrier))
		tenantPart := tenancy.Metadata{}
		for k, v := range carrier {
			if strings.HasPrefix(k, carrierTenantKeyPrefix) {
				tenantPart[k] = v
				continue
			}
			out[k] = v
		}
		if len(tenantPart) > 0 {
			// The carrier's own tenant is never an identity source: it must
			// agree with the attached one and is dropped from the wire.
			ct, err := tenancy.UnmarshalMetadata(tenantPart)
			if err != nil || !hasTenant || ct != tc {
				return reject("command metadata names a tenant other than the caller's")
			}
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return err
		}
		headers.Set(wireHeaderCommand, string(raw))
	}
	if len(headers.Values(wireHeaderTenant)) > 0 || len(headers.Values(wireHeaderCommand)) > 0 {
		headers.Set(wireHeaderVersion, wireVersion)
	}
	return nil
}

// Extract rebuilds the tenant identity and command carrier on the receiving
// node. No wire header at all returns ctx unchanged.
func (p tenantPropagator) Extract(ctx context.Context, headers nethttp.Header) (context.Context, error) {
	version, hasVersion, err := singleValue(headers, wireHeaderVersion)
	if err != nil {
		return ctx, err
	}
	tenantRaw, hasTenant, err := singleValue(headers, wireHeaderTenant)
	if err != nil {
		return ctx, err
	}
	commandRaw, hasCommand, err := singleValue(headers, wireHeaderCommand)
	if err != nil {
		return ctx, err
	}
	if !hasVersion && !hasTenant && !hasCommand {
		return ctx, nil
	}
	if !hasVersion || version != wireVersion {
		return ctx, reject("unsupported or missing wire version")
	}

	if hasTenant {
		tc, err := p.decodeTenant(tenantRaw)
		if err != nil {
			return ctx, err
		}
		if ctx, err = tenancy.Attach(ctx, tc); err != nil {
			return ctx, err
		}
	}
	if hasCommand {
		carrier, err := decodeCarrier(commandRaw)
		if err != nil {
			return ctx, err
		}
		ctx = protocol.AttachCarrier(ctx, carrier)
	}
	return ctx, nil
}

func (p tenantPropagator) decodeTenant(raw string) (tenancy.TenantContext, error) {
	var md map[string]string
	if err := json.Unmarshal([]byte(raw), &md); err != nil {
		return tenancy.TenantContext{}, reject("malformed tenant header")
	}
	tc, err := tenancy.UnmarshalMetadata(tenancy.Metadata(md))
	if err != nil {
		return tenancy.TenantContext{}, reject("invalid tenant header: %v", err)
	}
	if tc.Scope() != tenancy.ScopeTenant {
		return tenancy.TenantContext{}, reject("administrative scope is rejected on the wire")
	}
	if p.fixed != "" {
		if id, _ := tc.Tenant(); id != p.fixed {
			return tenancy.TenantContext{}, reject("tenant is not this single-tenant node's tenant")
		}
	}
	return tc, nil
}

func decodeCarrier(raw string) (command.Carrier, error) {
	var carrier command.Carrier
	if err := json.Unmarshal([]byte(raw), &carrier); err != nil {
		return nil, reject("malformed command header")
	}
	for k := range carrier {
		if strings.HasPrefix(k, carrierTenantKeyPrefix) {
			return nil, reject("command header must not carry a tenant")
		}
	}
	if _, err := command.UnmarshalMetadata(carrier); err != nil {
		return nil, reject("invalid command header: %v", err)
	}
	return carrier, nil
}

// singleValue returns the only value of key, failing on duplicates or an
// oversized value.
func singleValue(headers nethttp.Header, key string) (string, bool, error) {
	values := headers.Values(key)
	switch len(values) {
	case 0:
		return "", false, nil
	case 1:
		if len(values[0]) > maxWireValueBytes {
			return "", false, reject("%s exceeds %d bytes", key, maxWireValueBytes)
		}
		return values[0], true, nil
	default:
		return "", false, reject("duplicate %s header", key)
	}
}
