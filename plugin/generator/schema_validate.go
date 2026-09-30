// Copyright 2026 The Protobuf Project authors.
// SPDX-License-Identifier: Apache-2.0

package generator

import (
	"math"

	"buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// fieldRules returns the buf.validate rules declared on fd, or nil when there
// are none or they are switched off with IGNORE_ALWAYS.
func fieldRules(fd protoreflect.FieldDescriptor) *validate.FieldRules {
	if !proto.HasExtension(fd.Options(), validate.E_Field) {
		return nil
	}
	rules, _ := proto.GetExtension(fd.Options(), validate.E_Field).(*validate.FieldRules)
	if rules.GetIgnore() == validate.Ignore_IGNORE_ALWAYS {
		return nil
	}
	return rules
}

// isValidateRequired reports whether fd carries (buf.validate.field).required.
func isValidateRequired(fd protoreflect.FieldDescriptor) bool {
	return fieldRules(fd).GetRequired()
}

// openAIUnsupportedKeywords are validation keywords OpenAI strict Structured
// Outputs rejects; with strict mode on, one unsupported keyword fails the whole
// request, so these are dropped rather than risked.
var openAIUnsupportedKeywords = []string{"minLength", "maxLength", "uniqueItems", "minProperties", "maxProperties"}

// applyValidateRules adds the JSON Schema keywords that buf.validate rules on
// fd express to schema, the schema of a singular value of fd.
//
// Only rules JSON Schema can state exactly are mapped. CEL expressions,
// predefined rules and exclusive-outside ranges (gt > lt) are left to the
// server, which still enforces them.
func applyValidateRules(fd protoreflect.FieldDescriptor, schema map[string]any, openAI bool) {
	applyRules(fd, fieldRules(fd), schema, openAI)
}

// applyRules adds the keywords of rules to schema. fd supplies the value kind
// and, for enums, the value names; for repeated fields it is the element kind.
func applyRules(fd protoreflect.FieldDescriptor, rules *validate.FieldRules, schema map[string]any, openAI bool) {
	if rules == nil {
		return
	}
	constraints := scalarConstraints(fd, rules)
	if openAI {
		restrictToOpenAI(constraints)
	}
	if len(constraints) == 0 {
		return
	}
	// IGNORE_IF_ZERO_VALUE accepts the zero value unconditionally, so a schema
	// that only allowed constrained values would reject input the server takes.
	if rules.GetIgnore() == validate.Ignore_IGNORE_IF_ZERO_VALUE {
		zero, ok := zeroValue(fd)
		// OpenAI schemas have no const, and a nullable type union does not nest
		// cleanly under anyOf; leaving the field unconstrained stays correct.
		if ok && !openAI {
			schema["anyOf"] = []map[string]any{{"const": zero}, constraints}
		}
		return
	}
	for k, v := range constraints {
		schema[k] = v
	}
}

// restrictToOpenAI rewrites constraints to the OpenAI strict subset: a const
// becomes a one-value enum and unsupported keywords are removed.
func restrictToOpenAI(constraints map[string]any) {
	if v, ok := constraints["const"]; ok {
		delete(constraints, "const")
		constraints["enum"] = []any{v}
	}
	for _, k := range openAIUnsupportedKeywords {
		delete(constraints, k)
	}
	if format, ok := constraints["format"].(string); ok && !openAIFormats[format] {
		delete(constraints, "format")
	}
}

// openAIFormats are the string formats OpenAI strict Structured Outputs accepts.
var openAIFormats = map[string]bool{
	"date-time": true, "time": true, "date": true, "duration": true,
	"email": true, "hostname": true, "ipv4": true, "ipv6": true, "uuid": true,
}

// applyContainerRules adds the item or pair count keywords of fd's repeated or
// map rules to schema, the schema of the whole list or map.
func applyContainerRules(fd protoreflect.FieldDescriptor, schema map[string]any, openAI bool) {
	rules := fieldRules(fd)
	if r := rules.GetRepeated(); r != nil {
		if r.HasMinItems() {
			schema["minItems"] = r.GetMinItems()
		}
		if r.HasMaxItems() {
			schema["maxItems"] = r.GetMaxItems()
		}
		if r.GetUnique() {
			schema["uniqueItems"] = true
		}
	}
	if r := rules.GetMap(); r != nil {
		if r.HasMinPairs() {
			schema["minProperties"] = r.GetMinPairs()
		}
		if r.HasMaxPairs() {
			schema["maxProperties"] = r.GetMaxPairs()
		}
	}
	if openAI {
		restrictToOpenAI(schema)
	}
}

// itemRules returns the rules that apply to each element of a repeated field,
// or nil when the element rules are switched off with IGNORE_ALWAYS.
func itemRules(fd protoreflect.FieldDescriptor) *validate.FieldRules {
	items := fieldRules(fd).GetRepeated().GetItems()
	if items.GetIgnore() == validate.Ignore_IGNORE_ALWAYS {
		return nil
	}
	return items
}

func scalarConstraints(fd protoreflect.FieldDescriptor, rules *validate.FieldRules) map[string]any {
	switch {
	case rules.GetString() != nil:
		return stringConstraints(rules.GetString())
	case rules.GetEnum() != nil && fd.Kind() == protoreflect.EnumKind:
		return enumConstraints(fd.Enum(), rules.GetEnum())
	case rules.GetBool() != nil && rules.GetBool().HasConst():
		return map[string]any{"const": rules.GetBool().GetConst()}
	}
	switch kindToType(fd.Kind()) {
	case "integer":
		return numberConstraints(numericRules(rules), true)
	case "number":
		return numberConstraints(numericRules(rules), false)
	}
	return nil
}

func stringConstraints(r *validate.StringRules) map[string]any {
	c := map[string]any{}
	if r.HasConst() {
		c["const"] = r.GetConst()
	}
	if len(r.GetIn()) > 0 {
		c["enum"] = r.GetIn()
	}
	if r.HasLen() {
		c["minLength"] = r.GetLen()
		c["maxLength"] = r.GetLen()
	}
	if r.HasMinLen() {
		c["minLength"] = r.GetMinLen()
	}
	if r.HasMaxLen() {
		c["maxLength"] = r.GetMaxLen()
	}
	if r.HasPattern() {
		c["pattern"] = r.GetPattern()
	}
	if format := stringFormat(r); format != "" {
		c["format"] = format
	}
	return c
}

func stringFormat(r *validate.StringRules) string {
	switch {
	case r.GetEmail():
		return "email"
	case r.GetHostname():
		return "hostname"
	case r.GetIpv4():
		return "ipv4"
	case r.GetIpv6():
		return "ipv6"
	case r.GetUri():
		return "uri"
	case r.GetUriRef():
		return "uri-reference"
	case r.GetUuid():
		return "uuid"
	}
	return ""
}

// enumConstraints narrows the enum's value names to those the rules allow.
// defined_only needs no keyword: the schema already lists only defined values.
func enumConstraints(ed protoreflect.EnumDescriptor, r *validate.EnumRules) map[string]any {
	name := func(n int32) (string, bool) {
		v := ed.Values().ByNumber(protoreflect.EnumNumber(n))
		if v == nil {
			return "", false
		}
		return string(v.Name()), true
	}
	if r.HasConst() {
		if n, ok := name(r.GetConst()); ok {
			return map[string]any{"const": n}
		}
		return nil
	}
	if len(r.GetIn()) == 0 && len(r.GetNotIn()) == 0 {
		return nil
	}
	allowed := map[int32]bool{}
	if len(r.GetIn()) > 0 {
		for _, n := range r.GetIn() {
			allowed[n] = true
		}
	} else {
		for i := range ed.Values().Len() {
			allowed[int32(ed.Values().Get(i).Number())] = true
		}
	}
	for _, n := range r.GetNotIn() {
		delete(allowed, n)
	}
	var names []string
	for i := range ed.Values().Len() {
		v := ed.Values().Get(i)
		if allowed[int32(v.Number())] {
			names = append(names, string(v.Name()))
		}
	}
	return map[string]any{"enum": names}
}

// numericRules returns the populated numeric rule message (Int32Rules,
// DoubleRules, ...). They all share the const, in, less_than and greater_than
// shape, so one reflective reader covers every kind.
func numericRules(rules *validate.FieldRules) protoreflect.Message {
	m := rules.ProtoReflect()
	fd := m.WhichOneof(m.Descriptor().Oneofs().ByName("type"))
	if fd == nil || fd.Kind() != protoreflect.MessageKind {
		return nil
	}
	return m.Get(fd).Message()
}

func numberConstraints(r protoreflect.Message, integer bool) map[string]any {
	if r == nil {
		return nil
	}
	fields := r.Descriptor().Fields()
	if fd := fields.ByName("const"); fd != nil && r.Has(fd) {
		return map[string]any{"const": r.Get(fd).Interface()}
	}
	c := map[string]any{}
	if fd := fields.ByName("in"); fd != nil && r.Has(fd) {
		list := r.Get(fd).List()
		values := make([]any, list.Len())
		for i := range values {
			values[i] = list.Get(i).Interface()
		}
		c["enum"] = values
	}

	lowerField, lower, hasLower := bound(r, "greater_than")
	upperField, upper, hasUpper := bound(r, "less_than")
	// gt > lt means "outside the range", which is a disjunction JSON Schema
	// keywords cannot state without anyOf; leave it to the server.
	if hasLower && hasUpper && lower > upper {
		return c
	}
	if hasLower {
		setBound(c, lowerField == "gt", lower, integer, "minimum", "exclusiveMinimum", 1)
	}
	if hasUpper {
		setBound(c, upperField == "lt", upper, integer, "maximum", "exclusiveMaximum", -1)
	}
	return c
}

// bound reads the populated member of the named oneof (gt/gte or lt/lte).
func bound(r protoreflect.Message, oneof string) (protoreflect.Name, float64, bool) {
	od := r.Descriptor().Oneofs().ByName(protoreflect.Name(oneof))
	if od == nil {
		return "", 0, false
	}
	fd := r.WhichOneof(od)
	if fd == nil {
		return "", 0, false
	}
	switch v := r.Get(fd).Interface().(type) {
	case int32:
		return fd.Name(), float64(v), true
	case uint32:
		return fd.Name(), float64(v), true
	case float32:
		return fd.Name(), float64(v), true
	case float64:
		return fd.Name(), v, true
	}
	return "", 0, false
}

// setBound writes an inclusive or exclusive bound. Integer exclusive bounds
// become inclusive by stepping one towards the range, which keeps the schema
// usable by clients that only understand minimum and maximum.
func setBound(c map[string]any, exclusive bool, v float64, integer bool, inclusiveKey, exclusiveKey string, step float64) {
	switch {
	case !exclusive && integer:
		c[inclusiveKey] = int64(v)
	case !exclusive:
		c[inclusiveKey] = v
	case integer && !math.IsInf(v+step, 0):
		c[inclusiveKey] = int64(v + step)
	default:
		c[exclusiveKey] = v
	}
}

// zeroValue returns the JSON value protojson reads as fd's zero value.
func zeroValue(fd protoreflect.FieldDescriptor) (any, bool) {
	switch kindToType(fd.Kind()) {
	case "string":
		if fd.Kind() == protoreflect.EnumKind {
			v := fd.Enum().Values().ByNumber(0)
			if v == nil {
				return nil, false
			}
			return string(v.Name()), true
		}
		return "", true
	case "integer", "number":
		return 0, true
	case "boolean":
		return false, true
	}
	return nil, false
}
