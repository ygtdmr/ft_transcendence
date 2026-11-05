package unit_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/melmut-42/ft_transcendence/backend/shared/tracecontext"
)

const (
	goodTrace = "0af7651916cd43dd8448eb211c80319c"
	goodSpan  = "b7ad6b7169203331"
)

func TestTraceparentParsing(t *testing.T) {
	valid := map[string]byte{
		"00-" + goodTrace + "-" + goodSpan + "-01": 1,
		"00-" + goodTrace + "-" + goodSpan + "-00": 0,
		// A later version may add fields; the first four are still read.
		"01-" + goodTrace + "-" + goodSpan + "-01-extra": 1,
	}
	for header, flags := range valid {
		got, ok := tracecontext.Parse(header)
		if !ok || got.TraceID != goodTrace || got.SpanID != goodSpan || got.Flags != flags {
			t.Errorf("Parse(%q) = %+v, %v", header, got, ok)
		}
	}

	invalid := []string{
		"",
		"garbage",
		"ff-" + goodTrace + "-" + goodSpan + "-01",                  // version ff is forbidden
		"00-" + goodTrace + "-" + goodSpan + "-01-extra",            // version 00 has exactly four fields
		"00-" + goodTrace + "-" + goodSpan,                          // missing flags
		"00-" + strings.ToUpper(goodTrace) + "-" + goodSpan + "-01", // upper-case hex
		"00-" + goodTrace[:31] + "-" + goodSpan + "-01",             // short trace id
		"00-" + goodTrace + "0-" + goodSpan + "-01",                 // long trace id
		"00-" + goodTrace + "-" + goodSpan[:15] + "-01",             // short span id
		"00-" + strings.Repeat("0", 32) + "-" + goodSpan + "-01",    // all-zero trace id
		"00-" + goodTrace + "-" + strings.Repeat("0", 16) + "-01",   // all-zero span id
		"00-" + goodTrace + "-" + goodSpan + "-zz",                  // non-hex flags
		"0-" + goodTrace + "-" + goodSpan + "-01",                   // short version
		"00-" + goodTrace + "-" + goodSpan + "-01\r\nX-Evil: 1",     // header injection attempt
	}
	for _, header := range invalid {
		if got, ok := tracecontext.Parse(header); ok {
			t.Errorf("Parse(%q) accepted: %+v", header, got)
		}
	}
}

func TestTraceparentRoundTripAndChild(t *testing.T) {
	root := tracecontext.NewRoot()
	if len(root.TraceID) != 32 || len(root.SpanID) != 16 || root.ParentID != "" || root.Sampled() {
		t.Fatalf("root = %+v", root)
	}
	parsed, ok := tracecontext.Parse(root.Header())
	if !ok || parsed.TraceID != root.TraceID || parsed.SpanID != root.SpanID {
		t.Fatalf("Header/Parse round trip failed: %q -> %+v", root.Header(), parsed)
	}

	child := root.Child()
	if child.TraceID != root.TraceID || child.SpanID == root.SpanID || child.ParentID != root.SpanID {
		t.Errorf("child = %+v of %+v", child, root)
	}
	sampled := tracecontext.Context{TraceID: goodTrace, SpanID: goodSpan, Flags: 1}
	if !sampled.Child().Sampled() {
		t.Error("the sampled flag must be inherited")
	}
}

func TestTraceparentSelect(t *testing.T) {
	one := http.Header{}
	one.Set(tracecontext.Header, "00-"+goodTrace+"-"+goodSpan+"-01")
	got := tracecontext.Select(one)
	if got.TraceID != goodTrace || got.ParentID != goodSpan || got.SpanID == goodSpan {
		t.Errorf("a valid header must continue the trace in a new span: %+v", got)
	}

	bad := http.Header{}
	bad.Set(tracecontext.Header, "not-a-traceparent")
	repeated := http.Header{}
	repeated.Add(tracecontext.Header, "00-"+goodTrace+"-"+goodSpan+"-01")
	repeated.Add(tracecontext.Header, "00-"+goodTrace+"-"+goodSpan+"-01")
	for name, h := range map[string]http.Header{"missing": {}, "invalid": bad, "repeated": repeated} {
		root := tracecontext.Select(h)
		if root.ParentID != "" || (name != "missing" && root.TraceID == goodTrace) {
			t.Errorf("%s header must start a new root trace, got %+v", name, root)
		}
	}
}

func TestGeneratedTraceIDsAreUniqueAndNonZero(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		c := tracecontext.NewRoot()
		if seen[c.TraceID] || strings.Trim(c.TraceID, "0") == "" || strings.Trim(c.SpanID, "0") == "" {
			t.Fatalf("duplicate or zero id at iteration %d: %+v", i, c)
		}
		seen[c.TraceID] = true
	}
}
