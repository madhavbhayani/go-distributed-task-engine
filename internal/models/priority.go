package models

// ════════════════════════════════════════════════════════════════
//  Priority System
//
//  Scale: 1 (highest) → 10 (lowest)
//
//  Queue mapping:
//    HIGH   queue ← priorities 1, 2, 3
//    MEDIUM queue ← priorities 4, 5, 6, 7
//    LOW    queue ← priorities 8, 9, 10
// ════════════════════════════════════════════════════════════════

type Priority int

const (
	PriorityCritical Priority = 1 // Highest — immediate dispatch
	PriorityHigh     Priority = 2
	PriorityAboveAvg Priority = 3
	PriorityMedHigh  Priority = 4
	PriorityMedium   Priority = 5 // Default
	PriorityDefault  Priority = 5
	PriorityMedLow   Priority = 6
	PriorityBelowAvg Priority = 7
	PriorityLow      Priority = 8
	PriorityLower    Priority = 9
	PriorityLowest   Priority = 10 // Lowest — background tasks
)

// QueueTier represents the logical queue a job is routed to.
type QueueTier string

const (
	QueueTierHigh   QueueTier = "HIGH"
	QueueTierMedium QueueTier = "MEDIUM"
	QueueTierLow    QueueTier = "LOW"
)

// IsValid checks if the priority value is within acceptable range.
func (p Priority) IsValid() bool {
	return p >= 1 && p <= 10
}

// Tier returns the logical queue tier for this priority.
func (p Priority) Tier() QueueTier {
	switch {
	case p >= 1 && p <= 3:
		return QueueTierHigh
	case p >= 4 && p <= 7:
		return QueueTierMedium
	default:
		return QueueTierLow
	}
}

// Label returns a human-readable label for the priority.
func (p Priority) Label() string {
	switch {
	case p <= 1:
		return "CRITICAL"
	case p <= 3:
		return "HIGH"
	case p <= 7:
		return "MEDIUM"
	case p <= 9:
		return "LOW"
	default:
		return "LOWEST"
	}
}
