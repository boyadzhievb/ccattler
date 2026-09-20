package store

import "testing"

func TestFindPrefixRangeFindsMatchingBlock(t *testing.T) {
	facts := []Fact{
		{Key: "desired/service/api/image"},
		{Key: "desired/service/web/image"},
		{Key: "observed/instance/aaa/state"},
		{Key: "observed/instance/bbb/state"},
		{Key: "observed/instance/ccc/state"},
		{Key: "observed/node/node-1/state"},
		{Key: "placement/aaa"},
	}

	start, end := FindPrefixRange(facts, "observed/instance/")
	if start != 2 || end != 5 {
		t.Errorf("expected range [2,5), got [%d,%d)", start, end)
	}
}

func TestFindPrefixRangeNoMatch(t *testing.T) {
	facts := []Fact{
		{Key: "desired/service/api/image"},
		{Key: "observed/node/node-1/state"},
	}

	start, end := FindPrefixRange(facts, "placement/")
	if start != 0 || end != 0 {
		t.Errorf("expected empty range, got [%d,%d)", start, end)
	}
}

func TestFindPrefixRangeEmptySlice(t *testing.T) {
	start, end := FindPrefixRange(nil, "observed/")
	if start != 0 || end != 0 {
		t.Errorf("expected empty range for nil slice, got [%d,%d)", start, end)
	}
}

func TestFindPrefixRangeFirstPrefix(t *testing.T) {
	facts := []Fact{
		{Key: "desired/service/api/image"},
		{Key: "desired/service/web/image"},
		{Key: "observed/instance/aaa/state"},
	}

	start, end := FindPrefixRange(facts, "desired/")
	if start != 0 || end != 2 {
		t.Errorf("expected range [0,2), got [%d,%d)", start, end)
	}
}

func TestFindPrefixRangeLastPrefix(t *testing.T) {
	facts := []Fact{
		{Key: "desired/service/api/image"},
		{Key: "placement/aaa"},
		{Key: "placement/bbb"},
	}

	start, end := FindPrefixRange(facts, "placement/")
	if start != 1 || end != 3 {
		t.Errorf("expected range [1,3), got [%d,%d)", start, end)
	}
}

func TestFactsWithPrefixReturnsSubslice(t *testing.T) {
	facts := []Fact{
		{Key: "desired/service/api/image"},
		{Key: "observed/instance/aaa/state"},
		{Key: "observed/instance/bbb/state"},
		{Key: "placement/aaa"},
	}

	result := FactsWithPrefix(facts, "observed/instance/")
	if len(result) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(result))
	}
	if result[0].Key != "observed/instance/aaa/state" {
		t.Errorf("first key: got %s", result[0].Key)
	}
}

func TestFactsWithPrefixReturnsNilOnNoMatch(t *testing.T) {
	facts := []Fact{
		{Key: "desired/service/api/image"},
	}

	result := FactsWithPrefix(facts, "observed/")
	if result != nil {
		t.Errorf("expected nil for no match, got %d facts", len(result))
	}
}
