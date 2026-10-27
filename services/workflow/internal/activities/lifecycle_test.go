package activities

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// documentModelDir is the document service's model package — the source
// of truth for the lifecycle state machine and the state_changed payload
// that lifecycle.go mirrors (it cannot import another module's internal
// package).
const documentModelDir = "../../../document/internal/model/"

// documentConsts maps the document model's State*/Action* constant names to
// their string values (model/document.go).
func documentConsts(t *testing.T) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), documentModelDir+"document.go", nil, 0)
	if err != nil {
		t.Fatalf("parse document.go: %v", err)
	}
	out := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
			return true
		}
		lit, ok := vs.Values[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		name := vs.Names[0].Name
		if strings.HasPrefix(name, "State") || strings.HasPrefix(name, "Action") {
			v, _ := strconv.Unquote(lit.Value)
			out[name] = v
		}
		return true
	})
	return out
}

// TestLifecycleTransitionMirror_MatchesDocumentStateMachine pins every row
// of documentLifecycleTransitions to an identical row of the document
// service's lifecycleTransitions table (model/lifecycle.go). If the
// document state machine drops or retargets one of these transitions, the
// workflow activity must follow — this fails until it does.
func TestLifecycleTransitionMirror_MatchesDocumentStateMachine(t *testing.T) {
	consts := documentConsts(t)
	f, err := parser.ParseFile(token.NewFileSet(), documentModelDir+"lifecycle.go", nil, 0)
	if err != nil {
		t.Fatalf("parse lifecycle.go: %v", err)
	}
	ident := func(e ast.Expr) string {
		id, ok := e.(*ast.Ident)
		if !ok {
			return ""
		}
		return consts[id.Name]
	}
	real := map[lifecycleKey]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "lifecycleTransitions" {
			return true
		}
		for _, elt := range vs.Values[0].(*ast.CompositeLit).Elts {
			kv := elt.(*ast.KeyValueExpr)
			key := kv.Key.(*ast.CompositeLit)
			real[lifecycleKey{from: ident(key.Elts[0]), action: ident(key.Elts[1])}] = ident(kv.Value)
		}
		return false
	})
	if len(real) < 10 {
		t.Fatalf("parsed only %d transitions from lifecycle.go — parser is out of step with the source", len(real))
	}
	for k, to := range documentLifecycleTransitions {
		got, ok := real[k]
		if !ok {
			t.Errorf("mirrored transition %s --%s--> %s is not in the document state machine", k.from, k.action, to)
			continue
		}
		if got != to {
			t.Errorf("mirrored transition %s --%s--> %s: document state machine targets %s", k.from, k.action, to, got)
		}
	}
}

// TestStateChangedPayloadMirror_MatchesDocumentModel pins stateChangedPayload
// to the document service's DocumentStateChangedPayload (model/events.go):
// same fields, same json tags, so consumers of
// dms.document.state_changed.v1 read a workflow-driven change exactly like
// one made through the document API.
func TestStateChangedPayloadMirror_MatchesDocumentModel(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), documentModelDir+"events.go", nil, 0)
	if err != nil {
		t.Fatalf("parse events.go: %v", err)
	}
	real := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "DocumentStateChangedPayload" {
			return true
		}
		for _, fld := range ts.Type.(*ast.StructType).Fields.List {
			if fld.Tag == nil || len(fld.Names) == 0 {
				continue
			}
			tag, _ := strconv.Unquote(fld.Tag.Value)
			real[fld.Names[0].Name] = reflect.StructTag(tag).Get("json")
		}
		return false
	})
	if len(real) == 0 {
		t.Fatal("DocumentStateChangedPayload not found in events.go")
	}
	mirror := reflect.TypeOf(stateChangedPayload{})
	if mirror.NumField() != len(real) {
		t.Errorf("mirror has %d fields, document model has %d", mirror.NumField(), len(real))
	}
	for i := 0; i < mirror.NumField(); i++ {
		fld := mirror.Field(i)
		tag, ok := real[fld.Name]
		if !ok {
			t.Errorf("field %s is not in DocumentStateChangedPayload", fld.Name)
			continue
		}
		if got := fld.Tag.Get("json"); got != tag {
			t.Errorf("field %s: mirror json tag %q, document model has %q", fld.Name, got, tag)
		}
	}
}

func TestPlanLifecycle(t *testing.T) {
	type hop = lifecycleHop
	cases := []struct {
		from, target string
		want         []hop
		ok           bool
	}{
		{"in_review", "active", []hop{{"in_review", "approve", "active"}}, true},
		{"in_review", "draft", []hop{{"in_review", "reject", "draft"}}, true},
		{"draft", "active", []hop{{"draft", "submit_for_review", "in_review"}, {"in_review", "approve", "active"}}, true},
		{"draft", "draft", nil, true},
		{"active", "active", nil, true},
		{"active", "draft", nil, false},
		{"superseded", "active", nil, false},
		{"retained", "active", nil, false}, // restore is a records action, not an approval outcome
		{"archived", "active", nil, false},
		{"disposed", "draft", nil, false},
		{"in_review", "archived", nil, false},
		{"in_review", "in_review", nil, false}, // not a target the workflows set
		{"draft", "", nil, false},
	}
	for _, tc := range cases {
		got, ok := planLifecycle(tc.from, tc.target)
		if ok != tc.ok || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("planLifecycle(%q, %q) = %v, %v; want %v, %v", tc.from, tc.target, got, ok, tc.want, tc.ok)
		}
	}
}

// TestPlanRetention: the retention cron's targets walk the document state
// machine's archive / dispose rows (model/lifecycle.go:22-24, 26). active
// and superseded reach archived through retained; anything the state
// machine has no archive/dispose path from is refused.
func TestPlanRetention(t *testing.T) {
	type hop = lifecycleHop
	cases := []struct {
		from, target string
		want         []hop
		ok           bool
	}{
		{"retained", "archived", []hop{{"retained", "archive", "archived"}}, true},
		{"active", "archived", []hop{{"active", "archive", "retained"}, {"retained", "archive", "archived"}}, true},
		{"superseded", "archived", []hop{{"superseded", "archive", "retained"}, {"retained", "archive", "archived"}}, true},
		{"archived", "disposed", []hop{{"archived", "dispose", "disposed"}}, true},
		{"archived", "archived", nil, true}, // retry after a committed first attempt
		{"disposed", "disposed", nil, true},
		{"draft", "archived", nil, false},
		{"in_review", "archived", nil, false},
		{"disposed", "archived", nil, false},
		{"legal_hold", "archived", nil, false},
		{"active", "disposed", nil, false}, // dispose only from archived
		{"retained", "disposed", nil, false},
		{"active", "retained", nil, false}, // not a retention target
		{"active", "", nil, false},
	}
	for _, tc := range cases {
		got, ok := planRetention(tc.from, tc.target)
		if ok != tc.ok || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("planRetention(%q, %q) = %v, %v; want %v, %v", tc.from, tc.target, got, ok, tc.want, tc.ok)
		}
	}
}
