package bus

import (
	"strings"

	"github.com/54c1/niq/core/event"
)

// match checks whether an event satisfies a subscription pattern.
func match(p event.EventPattern, evt event.Event) bool {
	// Type match (supports "*" and "Prefix.*" wildcards).
	if !TypeMatches(p.Type, evt.Type) {
		return false
	}
	// SourceID filter: match if specified and different.
	if p.SourceID != "" && p.SourceID != evt.WorkerId {
		return false
	}
	// SourceType and Attributes are reserved for future use.
	return true
}

// TypeMatches checks wildcard-aware type matching.
// TypeMatches checks wildcard-aware type matching (* and Prefix.*).
func TypeMatches(pattern, eventType string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	if pattern == eventType {
		return true
	}
	if prefix, ok := strings.CutSuffix(pattern, ".*"); ok {
		return eventType == prefix || strings.HasPrefix(eventType, prefix+".")
	}
	return false
}
