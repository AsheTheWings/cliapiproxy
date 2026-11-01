//go:build unit

package pruning

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// bodyFor builds a request of a projection from its items and a model
// that stands for the rest.
func bodyFor(name string, model string, items ...string) []byte {
	list := "[" + strings.Join(items, ",") + "]"
	switch name {
	case "commandcode.messages":
		return []byte(`{"params":{"model":"` + model + `","messages":` + list + `},"threadId":"t"}`)
	case "cloudcode.contents":
		return []byte(`{"model":"` + model + `","request":{"contents":` + list + `,"generationConfig":{"temperature":1}}}`)
	case "responses.input":
		return []byte(`{"model":"` + model + `","input":` + list + `,"stream":true}`)
	case "messages":
		return []byte(`{"model":"` + model + `","messages":` + list + `,"max_tokens":64}`)
	default:
		return []byte(`{"contents":` + list + `,"generationConfig":{"model":"` + model + `"}}`)
	}
}

const (
	itemA = `{"role":"user","content":"a"}`
	itemB = `{"role":"assistant","content":"b"}`
	itemC = `{"role":"user","content":"c"}`
	// itemBReordered is itemB with its keys in another order.
	itemBReordered = `{"content":"b","role":"assistant"}`
)

func keptFull(t *testing.T, plan Plan, id int64) {
	t.Helper()
	for _, rep := range plan.Replacements {
		if rep.PayloadID == id {
			t.Fatalf("payload %d was pruned, want it kept full", id)
		}
	}
	for _, kept := range plan.KeptFull {
		if kept == id {
			return
		}
	}
	t.Fatalf("payload %d is not recorded as evaluated and kept full: %+v", id, plan)
}

// pruned checks that payload id became a residual of base which rebuilds
// the original body.
func pruned(t *testing.T, plan Plan, id, base int64, bodies map[int64][]byte) Replacement {
	t.Helper()
	for _, rep := range plan.Replacements {
		if rep.PayloadID != id {
			continue
		}
		if rep.BaseID != base {
			t.Fatalf("payload %d has base %d, want %d", id, rep.BaseID, base)
		}
		if err := Check(rep, bodies); err != nil {
			t.Fatalf("payload %d does not rebuild: %v", id, err)
		}
		return rep
	}
	t.Fatalf("payload %d was not pruned: %+v", id, plan)
	return Replacement{}
}

func decide(t *testing.T, rows ...Row) Plan {
	t.Helper()
	plan, err := Decide(rows)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestDecideEachProjection(t *testing.T) {
	for _, p := range projections {
		t.Run(p.name, func(t *testing.T) {
			newest := bodyFor(p.name, "m", itemA, itemB, itemC)

			prefix := bodyFor(p.name, "m", itemA, itemB)
			plan := decide(t, Row{ID: 2, Body: newest}, Row{ID: 1, Body: prefix})
			rep := pruned(t, plan, 1, 2, map[int64][]byte{1: prefix, 2: newest})
			if rep.RestSourceID != 2 {
				t.Errorf("exact prefix: rest source = %d, want the base", rep.RestSourceID)
			}
			keptFull(t, plan, 2)

			reordered := bodyFor(p.name, "m", itemA, itemBReordered)
			plan = decide(t, Row{ID: 2, Body: newest}, Row{ID: 1, Body: reordered})
			pruned(t, plan, 1, 2, map[int64][]byte{1: reordered, 2: newest})

			otherRest := bodyFor(p.name, "other-model", itemA, itemB)
			plan = decide(t, Row{ID: 2, Body: newest}, Row{ID: 1, Body: otherRest})
			rep = pruned(t, plan, 1, 2, map[int64][]byte{1: otherRest, 2: newest})
			if r, _ := ParseResidual(rep.Body); rep.RestSourceID != 0 || len(r.Rest) == 0 {
				t.Errorf("a rest that differs from the base's is not stored in full: %s", rep.Body)
			}

			for name, body := range map[string][]byte{
				"branch":           bodyFor(p.name, "m", itemA, itemC),
				"compaction":       bodyFor(p.name, "m", itemA, itemB, itemC, `{"role":"user","content":"d"}`),
				"empty list":       bodyFor(p.name, "m"),
				"duplicate keys":   bytes.Replace(prefix, []byte(`"model":"m"`), []byte(`"model":"m","model":"m"`), 1),
				"altered number":   bytes.Replace(prefix, []byte(`"model":"m"`), []byte(`"model":"m","n":0.10000000000000001`), 1),
				"large integer":    bytes.Replace(prefix, []byte(`"model":"m"`), []byte(`"model":"m","n":12345678901234567890`), 1),
				"lone surrogate":   bytes.Replace(prefix, []byte(`"content":"a"`), []byte(`"content":"\ud800"`), 1),
				"non-JSON":         []byte("not json"),
				"unprojectable":    []byte(`{"model":"m","prompt":"a"}`),
				"truncated prefix": prefix,
			} {
				// The newest input is shorter than a compaction's history,
				// so the compaction case is the newest input's extension.
				rows := []Row{{ID: 2, Body: newest}, {ID: 1, Body: body, Truncated: name == "truncated prefix"}}
				if name == "compaction" {
					rows = []Row{{ID: 2, Body: bodyFor(p.name, "m", `{"role":"user","content":"summary"}`)}, {ID: 1, Body: body}}
				}
				plan = decide(t, rows...)
				if len(plan.Replacements) != 0 {
					t.Errorf("%s: pruned %+v", name, plan.Replacements)
				}
				keptFull(t, plan, 1)
			}
		})
	}
}

func TestRebasingMovesResidualsAndKeepsThemRebuildable(t *testing.T) {
	const name = "responses.input"
	newest := bodyFor(name, "m", itemA, itemB, itemC)
	middle := bodyFor(name, "m", itemA, itemB)
	oldest := bodyFor(name, "old-model", itemA)
	// An earlier pass pruned the oldest input against the middle one,
	// inheriting nothing because its rest differs.
	first := decide(t, Row{ID: 2, Body: middle}, Row{ID: 1, Body: oldest})
	residual := pruned(t, first, 1, 2, map[int64][]byte{1: oldest, 2: middle})

	plan := decide(t, Row{ID: 3, Body: newest}, Row{ID: 2, Body: middle}, Row{ID: 1, Body: residual.Body, BaseID: 2, RestSourceID: residual.RestSourceID})
	rep := pruned(t, plan, 2, 3, map[int64][]byte{1: residual.Body, 2: middle, 3: newest})
	if len(rep.Dependents) != 1 || rep.Dependents[0].PayloadID != 1 || rep.Dependents[0].BaseID != 3 {
		t.Fatalf("dependents = %+v, want payload 1 moved to base 3", rep.Dependents)
	}
	moved, err := ParseResidual(rep.Dependents[0].Body)
	if err != nil || len(moved.Rest) == 0 {
		t.Fatalf("the moved residual lost its own rest: %s, %v", rep.Dependents[0].Body, err)
	}
	base, _ := Project(newest)
	rebuilt, err := Rebuild(moved, base, base.Rest)
	original, _ := Parse(oldest)
	if err != nil || !bytes.Equal(rebuilt, original.Canonical()) {
		t.Fatalf("the moved residual rebuilds %s, %v; want %s", rebuilt, err, original.Canonical())
	}

	// A residual that inherited the old base's rest, which differs from
	// the new base's, gets that rest in full.
	inherited := bodyFor(name, "mid-model", itemA)
	middleOther := bodyFor(name, "mid-model", itemA, itemB)
	firstInherited := decide(t, Row{ID: 2, Body: middleOther}, Row{ID: 1, Body: inherited})
	residual = pruned(t, firstInherited, 1, 2, map[int64][]byte{1: inherited, 2: middleOther})
	plan = decide(t, Row{ID: 3, Body: newest}, Row{ID: 2, Body: middleOther}, Row{ID: 1, Body: residual.Body, BaseID: 2, RestSourceID: 2})
	rep = pruned(t, plan, 2, 3, map[int64][]byte{1: residual.Body, 2: middleOther, 3: newest})
	if dep := rep.Dependents[0]; dep.RestSourceID != 0 {
		t.Fatalf("the inherited rest was not written in full: %+v", dep)
	}

	// A rest of its own that equals the new base's is inherited from it,
	// even when it holds characters JSON encoders often escape.
	marked := "<b> & \u2028"
	newestMarked := bodyFor(name, marked, itemA, itemB, itemC)
	oldestMarked := bodyFor(name, marked, itemA)
	first = decide(t, Row{ID: 2, Body: middle}, Row{ID: 1, Body: oldestMarked})
	residual = pruned(t, first, 1, 2, map[int64][]byte{1: oldestMarked, 2: middle})
	plan = decide(t, Row{ID: 3, Body: newestMarked}, Row{ID: 2, Body: middle}, Row{ID: 1, Body: residual.Body, BaseID: 2, RestSourceID: residual.RestSourceID})
	rep = pruned(t, plan, 2, 3, map[int64][]byte{1: residual.Body, 2: middle, 3: newestMarked})
	if dep := rep.Dependents[0]; dep.RestSourceID != 3 {
		t.Fatalf("a rest equal to the new base's was kept in full: %s", dep.Body)
	}
}

func TestMalformedResidualsAreRefused(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":        `{`,
		"unknown field":   `{"version":1,"projection":"messages","items":1,"canonical_sha256":"` + strings.Repeat("0", 64) + `","extra":1}`,
		"wrong version":   `{"version":2,"projection":"messages","items":1,"canonical_sha256":"` + strings.Repeat("0", 64) + `"}`,
		"no items":        `{"version":1,"projection":"messages","items":0,"canonical_sha256":"` + strings.Repeat("0", 64) + `"}`,
		"bad digest":      `{"version":1,"projection":"messages","items":1,"canonical_sha256":"xyz"}`,
		"unknown project": `{"version":1,"projection":"prompt","items":1,"canonical_sha256":"` + strings.Repeat("0", 64) + `"}`,
	} {
		if _, err := ParseResidual([]byte(body)); !errors.Is(err, ErrMalformedResidual) {
			t.Errorf("%s: err = %v, want ErrMalformedResidual", name, err)
		}
	}
	// A malformed residual based on an input the pass would prune fails the
	// conversation's evaluation.
	newest := bodyFor("messages", "m", itemA, itemB, itemC)
	middle := bodyFor("messages", "m", itemA, itemB)
	if _, err := Decide([]Row{{ID: 3, Body: newest}, {ID: 2, Body: middle}, {ID: 1, Body: []byte(`{`), BaseID: 2, RestSourceID: 2}}); !errors.Is(err, ErrMalformedResidual) {
		t.Fatalf("err = %v, want ErrMalformedResidual", err)
	}
}

func TestCheckRefusesAMismatchedRebuild(t *testing.T) {
	newest := bodyFor("messages", "m", itemA, itemB, itemC)
	prefix := bodyFor("messages", "m", itemA, itemB)
	plan := decide(t, Row{ID: 2, Body: newest}, Row{ID: 1, Body: prefix})
	// The stored input changed after planning.
	changed := bodyFor("messages", "m", itemA, itemC)
	if err := Check(plan.Replacements[0], map[int64][]byte{1: changed, 2: newest}); !errors.Is(err, ErrMismatch) {
		t.Fatalf("err = %v, want ErrMismatch", err)
	}
}
