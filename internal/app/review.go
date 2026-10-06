package app

import (
	"fmt"
	"time"

	"github.com/viktordanov/uah-core/harness/llm"

	"github.com/viktordanov/uah/internal/agents"
	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/review"
	"github.com/viktordanov/uah/internal/session"
)

// pickReview checks approvals_reviewer (auto_review by default, the
// owner's decision S8) and [review]: the model defaults to
// codex-auto-review on openai-codex and to the session model elsewhere, at
// low effort, with Codex's 90s timeout.
func pickReview(cfg config.Config, s session.Settings) (string, review.Config, error) {
	// The user answers unless the config asks for the reviewer; Auto mode
	// uses the reviewer whatever this says (approval.Mode).
	who := first(cfg.ApprovalsReviewer, review.ReviewerUser)
	if who != review.ReviewerAuto && who != review.ReviewerUser {
		return "", review.Config{}, usage(fmt.Errorf("invalid approvals_reviewer %q (want auto_review or user)", who))
	}
	effort := llm.ReasoningEffort(first(cfg.Review.Effort, string(review.DefaultEffort)))
	if !effort.Valid() {
		return "", review.Config{}, usage(fmt.Errorf("invalid review.effort %q", cfg.Review.Effort))
	}
	timeout := review.DefaultTimeout
	if cfg.Review.Timeout != "" {
		d, err := time.ParseDuration(cfg.Review.Timeout)
		if err != nil || d <= 0 {
			return "", review.Config{}, usage(fmt.Errorf("invalid review.timeout %q", cfg.Review.Timeout))
		}
		timeout = d
	}
	policyFile, err := promptPath("review.policy_file", cfg.Review.PolicyFile)
	if err != nil {
		return "", review.Config{}, err
	}

	return who, review.Config{
		GuardianMarkers: cfg.Review.GuardianMarkers,
		Model:           first(cfg.Review.Model, review.DefaultModel(s.Provider, s.Model)),
		Effort:          effort, Timeout: timeout, PolicyFile: policyFile,
	}, nil
}

// readReviewPolicy reads [review] policy_file into the reviewer's policy.
// As with experimental_compact_prompt_file, a missing or empty file is an
// error.
func readReviewPolicy(r *Resolved) error {
	if r.Review.PolicyFile == "" {
		return nil
	}
	text, err := readPrompt("review.policy_file", r.Review.PolicyFile)
	r.Review.Policy = text

	return err
}

// pickReviewLimits checks the /review limits: review_time_limit,
// review_token_limit, and review_command_timeout, with the defaults from
// agents.DefaultReviewCommand; the time and token limits are off unless
// set. 0 sets no limit.
func pickReviewLimits(cfg config.Config) (agents.ReviewLimits, error) {
	l := agents.ReviewLimits{Command: agents.DefaultReviewCommand}
	for _, d := range []struct {
		key, value string
		to         *time.Duration
	}{{"review_time_limit", cfg.ReviewTimeLimit, &l.Time}, {"review_command_timeout", cfg.ReviewCommandTimeout, &l.Command}} {
		if d.value == "" {
			continue
		}
		v, err := time.ParseDuration(d.value)
		if err != nil || v < 0 {
			return agents.ReviewLimits{}, usage(fmt.Errorf("invalid %s %q (want a duration such as 30m, or 0 for none)", d.key, d.value))
		}
		*d.to = v
	}
	if n := cfg.ReviewTokenLimit; n != nil {
		if *n < 0 {
			return agents.ReviewLimits{}, usage(fmt.Errorf("invalid review_token_limit %d (want 0 or more)", *n))
		}
		l.Tokens = *n
	}

	return l, nil
}
