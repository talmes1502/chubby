package model

import (
	"reflect"
	"testing"
)

func TestFilterSessions_EmptyQueryReturnsAll(t *testing.T) {
	in := []Session{
		{Name: "api"}, {Name: "worker"},
	}
	got := filterSessions(in, "")
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("empty query should return input slice, got %+v", got)
	}
}

func TestFilterSessions_CaseInsensitiveSubstring(t *testing.T) {
	in := []Session{
		{Name: "API-server"}, {Name: "worker"}, {Name: "Apricot"},
	}
	got := filterSessions(in, "api")
	want := []Session{{Name: "API-server"}, {Name: "Apricot"}}
	// Apricot doesn't contain 'api' literally but lowercase 'apricot' has 'apr', no 'api'.
	// Wait — "apricot" lowercase is "apricot", doesn't contain "api". Let's check.
	// 'a','p','r','i','c','o','t' — substring 'api' is 'a','p','i' — not present.
	// So want is just API-server.
	want = []Session{{Name: "API-server"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestFilterSessions_NoMatches(t *testing.T) {
	in := []Session{{Name: "api"}, {Name: "worker"}}
	got := filterSessions(in, "zzz")
	if len(got) != 0 {
		t.Fatalf("expected empty, got %+v", got)
	}
}
