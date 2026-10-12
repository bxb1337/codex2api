package promptfilter

import (
	"regexp"
	"testing"
)

func TestBuiltinRegexSharesProgramsWithoutSharingRuleState(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Advanced.Enforcement.AuthorizedPentestAllowed = false
	left := newRegexOwnershipEngine(t, cfg)
	cfg.Threshold++
	cfg.Advanced.Enforcement.AuthorizedPentestAllowed = true
	right := newRegexOwnershipEngine(t, cfg)
	shared := 0
	for index := range left.patterns {
		first, second := &left.patterns[index], &right.patterns[index]
		if first.re != nil {
			if first.re != second.re {
				t.Fatalf("builtin program not shared: %s", first.cfg.Name)
			}
			shared++
		}
		weight := second.cfg.Weight
		first.cfg.Weight = 0
		if weight != second.cfg.Weight {
			t.Fatal("rule weight leaked between engines")
		}
		checkRegexSliceOwnership(t, first.all, second.all)
		checkRegexSliceOwnership(t, first.any, second.any)
	}
	if shared == 0 {
		t.Fatal("no shared builtin program inspected")
	}
}

func checkRegexSliceOwnership(t *testing.T, left, right []*regexp.Regexp) {
	t.Helper()
	if len(left) == 0 {
		return
	}
	if len(left) != len(right) || left[0] != right[0] {
		t.Fatal("builtin list did not share its regex program")
	}
	program := right[0]
	left[0] = nil
	if program != right[0] {
		t.Fatal("regex list storage leaked between engines")
	}
}

func TestCustomRegexPreservesInputAndPrivatePrograms(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CustomPatterns = []PatternConfig{{Name: "owned_custom_fixture", Pattern: `owned_custom_fixture`, Weight: DefaultThreshold}}
	before := MarshalCustomPatterns(cfg.CustomPatterns)
	left := newRegexOwnershipEngine(t, cfg)
	right := newRegexOwnershipEngine(t, cfg)
	if before != MarshalCustomPatterns(cfg.CustomPatterns) {
		t.Fatal("input rule snapshot changed")
	}
	first, second := left.patterns[len(left.patterns)-1], right.patterns[len(right.patterns)-1]
	if first.cfg.Name != "owned_custom_fixture" || second.cfg.Name != "owned_custom_fixture" {
		t.Fatal("custom rule missing")
	}
	if first.re == second.re {
		t.Fatal("custom program entered the builtin cache")
	}
}

func newRegexOwnershipEngine(t *testing.T, cfg Config) *Engine {
	t.Helper()
	engine, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}
