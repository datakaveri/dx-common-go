package client

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

// Shadow runs an AuthZEN evaluation ALONGSIDE the legacy decision and reports
// divergence, without ever affecting the enforced request. It is the Stage-B
// migration-safety mechanism (plan §12.3): the legacy path stays authoritative
// while AuthZEN is proven to match on every relationship-only golden case before
// enforcement moves.
//
// Compare runs on a background context so a slow or failing PDP cannot add
// latency to, or fail, the request the PEP is actually serving.
type Shadow struct {
	eval    Evaluator
	timeout time.Duration
	onDiff  func(ShadowResult)
	log     *zap.Logger
}

// ShadowResult is one comparison outcome.
type ShadowResult struct {
	Legacy   bool
	AuthZEN  bool
	Diverged bool
	// Err is set when the AuthZEN evaluation itself failed (counted as a
	// divergence to be investigated, never as an allow).
	Err        error
	ReasonCode decision.ReasonCode
}

// NewShadow builds a shadow comparator. onDiff (may be nil) is called for every
// comparison — wire it to a metric/counter. timeout<=0 defaults to 2s.
func NewShadow(eval Evaluator, timeout time.Duration, log *zap.Logger, onDiff func(ShadowResult)) *Shadow {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Shadow{eval: eval, timeout: timeout, onDiff: onDiff, log: log}
}

// Compare launches a background AuthZEN evaluation and compares it to the legacy
// decision. It returns immediately; the caller's enforced path is unaffected.
func (s *Shadow) Compare(legacy bool, req decision.EvaluationRequest) {
	if s == nil || s.eval == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		defer cancel()
		res := ShadowResult{Legacy: legacy}
		resp, err := s.eval.Evaluate(ctx, req)
		if err != nil {
			// A failed shadow evaluation is a divergence to investigate, never a
			// silent match. It does NOT fail the request — that already used the
			// legacy result.
			res.Err = err
			res.Diverged = true
			s.log.Warn("authzen shadow evaluation failed",
				zap.Bool("legacy_allow", legacy), zap.Error(err))
			s.emit(res)
			return
		}
		res.AuthZEN = resp.Allowed()
		if dx := resp.DXContext(); dx != nil {
			res.ReasonCode = dx.ReasonCode
		}
		res.Diverged = res.AuthZEN != res.Legacy
		if res.Diverged {
			s.log.Warn("authzen shadow divergence",
				zap.Bool("legacy_allow", res.Legacy),
				zap.Bool("authzen_allow", res.AuthZEN),
				zap.String("reason_code", string(res.ReasonCode)),
				zap.String("subject", req.Subject.ID),
				zap.String("action", req.Action.Name),
				zap.String("resource", req.Resource.Type+":"+req.Resource.ID))
		}
		s.emit(res)
	}()
}

func (s *Shadow) emit(r ShadowResult) {
	if s.onDiff != nil {
		s.onDiff(r)
	}
}
