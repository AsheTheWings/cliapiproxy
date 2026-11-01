package pruning

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/AsheTheWings/cliapiproxy/internal/telemetry"
)

// EvaluationVersion is the evaluator's version. Raising it makes the next
// pass evaluate every full input again.
const EvaluationVersion = 1

// QuarantineFailures is how many consecutive failed evaluations leave a
// conversation's inputs full until it gains an input or the evaluator
// changes.
const QuarantineFailures = 3

// Kinds are the payload kinds a pass prunes.
var Kinds = []string{"client_input", "upstream_input"}

// Store is the pass's access to stored inputs.
type Store interface {
	// Lock takes the store-wide pruning lock without waiting; ok is false
	// when another pass holds it.
	Lock(ctx context.Context) (unlock func(), ok bool, err error)
	// MarkWithoutConversation records the evaluation of inputs that have
	// no conversation ID, which stay full.
	MarkWithoutConversation(ctx context.Context, version int) error
	// Conversations returns up to limit conversations with inputs that
	// need evaluation, oldest activity first, leaving out skip.
	Conversations(ctx context.Context, version, limit int, skip []string) ([]string, error)
	// Rows returns a conversation's inputs of one kind, newest first.
	Rows(ctx context.Context, conversation, kind string) ([]Row, error)
	// Apply checks and commits one replacement in one transaction and
	// returns the stored bytes it released.
	Apply(ctx context.Context, rep Replacement, version int) (released int64, err error)
	// Evaluated records a successful evaluation: every full input of the
	// conversation and kind it read is marked with version and has no
	// failures. ids are the inputs that stayed full.
	Evaluated(ctx context.Context, conversation, kind string, ids []int64, version int) error
	// Failed counts a failed evaluation on every input of the conversation
	// that needed it, of either kind, and quarantines them at
	// QuarantineFailures, returning how many it quarantined by kind.
	Failed(ctx context.Context, conversation string, version int) (quarantined map[string]int64, err error)
}

// Pass outcomes.
const (
	OutcomeOK      = "ok"
	OutcomePartial = "partial"
	OutcomeError   = "error"
)

// Result is what one pass did.
type Result struct {
	Outcome     string
	Pruned      map[string]int64
	Released    map[string]int64
	Quarantined map[string]int64
	// Failed lists the conversations whose evaluation failed.
	Failed []string
	// ErrorClass is the error class of the pass's first failure, or "".
	ErrorClass string
}

// Run runs one pass over every conversation with inputs to evaluate, in
// batches of batch conversations, and records its span and counters.
func Run(ctx context.Context, store Store, batch int) Result {
	ctx, finish := telemetry.StartPrunePass(ctx)
	res := Result{Outcome: OutcomeOK, Pruned: map[string]int64{}, Released: map[string]int64{}, Quarantined: map[string]int64{}}
	err := run(ctx, store, batch, &res)
	switch {
	case err != nil:
		res.Outcome = OutcomeError
		if res.ErrorClass == "" {
			res.ErrorClass = errorClass(err)
		}
	case len(res.Failed) > 0:
		res.Outcome = OutcomePartial
	}
	for _, kind := range Kinds {
		telemetry.RecordPruned(ctx, kind, res.Pruned[kind], res.Released[kind], res.Quarantined[kind])
	}
	finish(res.Outcome, res.ErrorClass)
	return res
}

// errorClass maps a pruning failure to its error class.
func errorClass(err error) string {
	switch {
	case errors.Is(err, ErrMismatch):
		return telemetry.ErrorResidualMismatch
	case errors.Is(err, ErrMalformedResidual):
		return telemetry.ErrorMalformedResidual
	}
	return telemetry.ErrorClass(err)
}

func run(ctx context.Context, store Store, batch int, res *Result) error {
	unlock, ok, err := store.Lock(ctx)
	if err != nil || !ok {
		return err
	}
	defer unlock()
	if err = store.MarkWithoutConversation(ctx, EvaluationVersion); err != nil {
		return err
	}
	var seen []string
	for {
		conversations, errBatch := store.Conversations(ctx, EvaluationVersion, batch, seen)
		if errBatch != nil {
			return errBatch
		}
		for _, conversation := range conversations {
			seen = append(seen, conversation)
			for _, kind := range Kinds {
				errEval := evaluate(ctx, store, conversation, kind, res)
				if errEval == nil {
					continue
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				res.Failed = append(res.Failed, conversation)
				if res.ErrorClass == "" {
					res.ErrorClass = errorClass(errEval)
				}
				quarantined, errFailed := store.Failed(ctx, conversation, EvaluationVersion)
				if errFailed != nil {
					return errFailed
				}
				for k, n := range quarantined {
					res.Quarantined[k] += n
				}
				break
			}
		}
		if len(conversations) < batch {
			return nil
		}
	}
}

func evaluate(ctx context.Context, store Store, conversation, kind string, res *Result) error {
	rows, err := store.Rows(ctx, conversation, kind)
	if err != nil {
		return err
	}
	plan, err := Decide(rows)
	if err != nil {
		return err
	}
	for _, rep := range plan.Replacements {
		released, errApply := store.Apply(ctx, rep, EvaluationVersion)
		if errApply != nil {
			return fmt.Errorf("payload %d: %w", rep.PayloadID, errApply)
		}
		res.Pruned[kind]++
		res.Released[kind] += released
	}
	return store.Evaluated(ctx, conversation, kind, plan.KeptFull, EvaluationVersion)
}

// Ticker delivers the schedule's ticks.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type timeTicker struct{ *time.Ticker }

func (t timeTicker) C() <-chan time.Time { return t.Ticker.C }

// NewTimeTicker ticks every interval on the wall clock.
func NewTimeTicker(interval time.Duration) Ticker { return timeTicker{time.NewTicker(interval)} }

// Schedule runs pass once per tick, the first tick one interval after
// Start; a tick that arrives while a pass runs starts none.
type Schedule struct {
	stop context.CancelFunc
	wg   sync.WaitGroup
}

// ErrNoInterval marks a schedule without a positive interval.
var ErrNoInterval = errors.New("pruning interval is 0")

// StartSchedule starts running pass on ticker's ticks until Stop. An
// interval of 0 runs nothing and returns ErrNoInterval.
func StartSchedule(interval time.Duration, newTicker func(time.Duration) Ticker, pass func(context.Context)) (*Schedule, error) {
	if interval <= 0 {
		return nil, ErrNoInterval
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Schedule{stop: cancel}
	ticker := newTicker(interval)
	running := make(chan struct{}, 1)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C():
				select {
				case running <- struct{}{}:
				default:
					continue
				}
				s.wg.Add(1)
				go func() {
					defer s.wg.Done()
					defer func() { <-running }()
					pass(ctx)
				}()
			}
		}
	}()
	return s, nil
}

// Stop ends the schedule, cancels a running pass, and waits for it.
func (s *Schedule) Stop() {
	if s == nil {
		return
	}
	s.stop()
	s.wg.Wait()
}
