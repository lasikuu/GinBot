// Package trigger implements the pure matching engine behind GinBot's custom
// auto-responders: pattern construction, chance weighting, spoiler stripping,
// candidate selection, the forced-fire rate limiter and the per-instance
// compiled-set cache.
//
// It deliberately imports no database and no network client — only pb, for
// pb.TriggerMode — so that matching is a pure function of its inputs. This is
// the first genuinely hot path in the project: it runs on every message.
package trigger

import (
	"errors"
	"time"
)

const (
	// DefaultChance is the percentage a trigger fires at when it stores 0.
	// Carried from the old bot, where a stored 0 meant "use the default".
	DefaultChance int32 = 5

	// ExactChanceMultiplier weights exact-mode triggers, which are rarer and
	// more deliberate than any-mode ones. Also carried from the old bot: an
	// exact trigger stored at the default therefore fires at 15%.
	ExactChanceMultiplier int32 = 3

	// MaxChance clamps the effective chance.
	MaxChance int32 = 100

	// MaxPatternLength caps a stored phrase. A regex-mode phrase is run against
	// every message, so an unbounded pattern is a denial-of-service surface.
	MaxPatternLength = 200

	// MaxCandidates caps how many compiled triggers are evaluated for one
	// message, bounding the per-message cost regardless of how many an
	// instance accumulates.
	MaxCandidates = 500

	// ForcedInterval is the minimum gap between two forced fires by one
	// author.
	ForcedInterval = 60 * time.Second
)

// ErrPatternTooLong is returned for a phrase longer than MaxPatternLength.
var ErrPatternTooLong = errors.New("phrase exceeds the maximum pattern length")

// ErrEmptyPhrase is returned for a blank phrase.
var ErrEmptyPhrase = errors.New("phrase is empty")
