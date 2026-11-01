package pruning

import (
	"bytes"
	"crypto/sha256"
	"fmt"
)

// Row is one stored input of a conversation and kind, its body
// decompressed.
type Row struct {
	ID        int64
	Body      []byte
	Truncated bool
	// BaseID and RestSourceID are 0 for a full input.
	BaseID       int64
	RestSourceID int64
}

// Full reports an input stored in full.
func (r Row) Full() bool { return r.BaseID == 0 }

// Rewrite is a payload's new residual.
type Rewrite struct {
	PayloadID    int64
	BaseID       int64
	RestSourceID int64
	Body         []byte
}

// Replacement turns one full input into a residual and moves the residuals
// based on it to its new base. It commits as one unit.
type Replacement struct {
	Rewrite
	Dependents []Rewrite
}

// Plan is what one evaluation of a conversation and kind decided.
type Plan struct {
	// Replacements are in the order they must commit.
	Replacements []Replacement
	// KeptFull are the full inputs that stay full.
	KeptFull []int64
}

type leaf struct {
	id         int64
	transcript *Transcript
}

// chain returns, for each item, the key of the list's prefix ending at it.
func chain(t *Transcript) []string {
	keys := make([]string, len(t.Items))
	h := sha256.Sum256([]byte("cliapiproxy.prefix.v1\x00" + t.Projection))
	for i, item := range t.Items {
		itemSum := sha256.Sum256(item)
		h = sha256.Sum256(append(h[:], itemSum[:]...))
		keys[i] = string(h[:])
	}
	return keys
}

// residualFor describes input t against base: the base's items it shares
// and its rest, omitted when it equals the base's.
func residualFor(t *Transcript, items int, rest []byte, base *leaf) (Residual, int64) {
	r := Residual{Version: ResidualVersion, Projection: t.Projection, Items: items, SHA256: t.SHA256}
	if bytes.Equal(rest, base.transcript.Rest.Canonical()) {
		return r, base.id
	}
	r.Rest = rest
	return r, 0
}

// Decide plans a conversation's inputs of one kind, given newest first.
// Each full input whose item list equals the leading items of a later full
// input becomes a residual of the newest such input; residuals based on it
// move to that input. An input that is truncated, outside RFC 8785, without
// an item list, or with an empty one stays full and is no base.
func Decide(rows []Row) (Plan, error) {
	var plan Plan
	prefixes := map[string]*leaf{}
	dependents := map[int64][]Row{}
	for _, row := range rows {
		if !row.Full() {
			dependents[row.BaseID] = append(dependents[row.BaseID], row)
			if row.RestSourceID != 0 && row.RestSourceID != row.BaseID {
				return Plan{}, fmt.Errorf("%w: payload %d takes its rest from %d, not its base", ErrMalformedResidual, row.ID, row.RestSourceID)
			}
		}
	}
	for _, row := range rows {
		if !row.Full() {
			continue
		}
		var t *Transcript
		if !row.Truncated {
			t, _ = Project(row.Body)
		}
		if t == nil || len(t.Items) == 0 {
			plan.KeptFull = append(plan.KeptFull, row.ID)
			continue
		}
		keys := chain(t)
		base, ok := prefixes[keys[len(keys)-1]]
		if !ok {
			l := &leaf{id: row.ID, transcript: t}
			for _, key := range keys {
				if _, seen := prefixes[key]; !seen {
					prefixes[key] = l
				}
			}
			plan.KeptFull = append(plan.KeptFull, row.ID)
			continue
		}
		residual, restSource := residualFor(t, len(t.Items), t.Rest.Canonical(), base)
		rep := Replacement{Rewrite: Rewrite{PayloadID: row.ID, BaseID: base.id, RestSourceID: restSource, Body: residual.encode()}}
		for _, dep := range dependents[row.ID] {
			moved, err := rebase(dep, t, base)
			if err != nil {
				return Plan{}, err
			}
			rep.Dependents = append(rep.Dependents, moved)
		}
		plan.Replacements = append(plan.Replacements, rep)
	}
	return plan, nil
}

// rebase moves a residual from old, which becomes a residual itself, to
// old's new base, writing its rest in full when it differs from the new
// base's.
func rebase(dep Row, old *Transcript, base *leaf) (Rewrite, error) {
	r, err := ParseResidual(dep.Body)
	if err != nil {
		return Rewrite{}, fmt.Errorf("payload %d: %w", dep.ID, err)
	}
	if r.Projection != old.Projection || r.Items > len(old.Items) {
		return Rewrite{}, fmt.Errorf("%w: payload %d does not fit its base", ErrMalformedResidual, dep.ID)
	}
	rest := []byte(r.Rest)
	if len(rest) == 0 {
		rest = old.Rest.Canonical()
	}
	moved, restSource := residualFor(old, r.Items, rest, base)
	moved.SHA256 = r.SHA256
	return Rewrite{PayloadID: dep.ID, BaseID: base.id, RestSourceID: restSource, Body: moved.encode()}, nil
}

// Check verifies a replacement against the current bodies of its payload,
// its base, and its dependents: each rebuilds, from its new residual and
// base, the request it stood for. It changes nothing.
func Check(rep Replacement, bodies map[int64][]byte) error {
	original, err := Project(bodies[rep.PayloadID])
	if err != nil {
		return fmt.Errorf("payload %d: %w", rep.PayloadID, err)
	}
	base, err := Project(bodies[rep.BaseID])
	if err != nil {
		return fmt.Errorf("base %d: %w", rep.BaseID, err)
	}
	if err = verify(rep.Rewrite, base, original.SHA256); err != nil {
		return err
	}
	for _, dep := range rep.Dependents {
		before, errParse := ParseResidual(bodies[dep.PayloadID])
		if errParse != nil {
			return fmt.Errorf("payload %d: %w", dep.PayloadID, errParse)
		}
		if _, err = Rebuild(before, original, original.Rest); err != nil {
			return fmt.Errorf("payload %d before rebasing: %w", dep.PayloadID, err)
		}
		if err = verify(dep, base, before.SHA256); err != nil {
			return err
		}
	}
	return nil
}

// verify rebuilds a rewrite from base and requires the request's digest.
func verify(w Rewrite, base *Transcript, want string) error {
	r, err := ParseResidual(w.Body)
	if err != nil {
		return fmt.Errorf("payload %d: %w", w.PayloadID, err)
	}
	if r.SHA256 != want {
		return fmt.Errorf("payload %d: %w", w.PayloadID, ErrMismatch)
	}
	if _, err = Rebuild(r, base, base.Rest); err != nil {
		return fmt.Errorf("payload %d: %w", w.PayloadID, err)
	}
	return nil
}
