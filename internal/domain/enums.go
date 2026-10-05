package domain

import (
	"slices"
	"strings"
)

// ErrorCategory is what kind of failure a log describes.
type ErrorCategory string

// The known error categories.
const (
	CategoryDatabaseTimeout       ErrorCategory = "database_timeout"
	CategoryNullPointer           ErrorCategory = "null_pointer"
	CategoryRateLimit             ErrorCategory = "rate_limit_exceeded"
	CategoryAuthFailure           ErrorCategory = "auth_failure"
	CategoryNetworkPartialFailure ErrorCategory = "network_partial_failure"
	CategoryValidationError       ErrorCategory = "validation_error"
	CategoryUnknown               ErrorCategory = "unknown"
)

var errorCategories = []ErrorCategory{
	CategoryDatabaseTimeout, CategoryNullPointer, CategoryRateLimit, CategoryAuthFailure,
	CategoryNetworkPartialFailure, CategoryValidationError, CategoryUnknown,
}

// ErrorCategories returns every category, in the order the prompts present
// them. The caller gets a copy.
func ErrorCategories() []ErrorCategory { return slices.Clone(errorCategories) }

// Valid reports whether c is one of the known categories.
func (c ErrorCategory) Valid() bool { return slices.Contains(errorCategories, c) }

// Priority is how urgently an incident needs attention.
type Priority string

// The known priorities, most urgent first.
const (
	PriorityCritical Priority = "critical"
	PriorityHigh     Priority = "high"
	PriorityMedium   Priority = "medium"
	PriorityLow      Priority = "low"
)

var priorities = []Priority{PriorityCritical, PriorityHigh, PriorityMedium, PriorityLow}

// Priorities returns every priority, most urgent first. The caller gets a copy.
func Priorities() []Priority { return slices.Clone(priorities) }

// Valid reports whether p is one of the known priorities.
func (p Priority) Valid() bool { return slices.Contains(priorities, p) }

// Environment is where an error happened.
type Environment string

// The known environments.
const (
	EnvDev     Environment = "dev"
	EnvStaging Environment = "staging"
	EnvProd    Environment = "prod"
)

var environments = []Environment{EnvDev, EnvStaging, EnvProd}

// Environments returns every environment. The caller gets a copy.
func Environments() []Environment { return slices.Clone(environments) }

// Valid reports whether e is one of the known environments.
func (e Environment) Valid() bool { return slices.Contains(environments, e) }

// ParseEnvironment converts user input to an Environment. Matching is
// case-insensitive, and "development" and "production" are accepted as long
// forms of "dev" and "prod".
func ParseEnvironment(s string) (Environment, bool) {
	switch strings.ToLower(s) {
	case "dev", "development":
		return EnvDev, true
	case "staging":
		return EnvStaging, true
	case "prod", "production":
		return EnvProd, true
	}
	return "", false
}
