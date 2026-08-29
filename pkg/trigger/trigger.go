// Package trigger is the matching engine behind GinBot's auto-responders. It
// runs on every message.
package trigger

import (
	"errors"
	"time"
)

const (
	// DefaultChance is the percentage a trigger fires at when it stores 0.
	DefaultChance int32 = 5

	ExactChanceMultiplier int32 = 3

	MaxChance int32 = 100

	// MaxPatternLength bounds a regex-mode phrase, run against every message.
	MaxPatternLength = 200

	MaxCandidates = 500

	// ForcedInterval is the minimum gap between two forced fires by one author.
	ForcedInterval = 60 * time.Second
)

var ErrPatternTooLong = errors.New("phrase exceeds the maximum pattern length")

var ErrEmptyPhrase = errors.New("phrase is empty")
