package protocol

import (
	"reflect"

	"google.golang.org/protobuf/proto"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/persistence"
)

// DefinitionOf identifies a behavior's logical definition. Applications that
// reuse one Go type for distinct definitions can supply a stable DefinitionID.
// Protobuf definitions use their full wire name; local ones use package/type.
func DefinitionOf(behavior any) string {
	if defined, ok := behavior.(interface{ DefinitionID() string }); ok {
		return defined.DefinitionID()
	}
	if message, ok := behavior.(proto.Message); ok {
		return string(message.ProtoReflect().Descriptor().FullName())
	}
	typ := reflect.TypeOf(behavior)
	if typ == nil {
		return ""
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return typ.PkgPath() + "." + typ.Name()
}

// AnswerActorBinding compares the immutable actor binding with a request. It
// never runs business behavior or returns the identity of a different tenant.
func AnswerActorBinding(tenantAware bool, scope persistence.Scope, family string, behavior any, namespace string, query *egopb.ActorBindingQuery) *egopb.ActorBindingReply {
	matches := query.GetTenantAware() == tenantAware && query.GetFamily() == family && query.GetDefinition() == DefinitionOf(behavior) && query.GetNamespace() == namespace
	if tenantAware {
		matches = matches && scope.Valid() && !scope.IsUnscoped() && string(scope.TenantID()) == query.GetTenantId()
	} else {
		matches = matches && query.GetTenantId() == ""
	}
	return &egopb.ActorBindingReply{Matches: matches}
}
