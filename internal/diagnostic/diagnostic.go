// Package diagnostic classifies an evaluated verdict into a documented RG
// code. It never discovers a violation; it names one an oracle already found.
package diagnostic

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/weavegate/weavegate/internal/oracle"
)

var codePattern = regexp.MustCompile(`^RG[0-9]{3}$`)

type Code string
type Severity string
type Trigger string

const (
	SeverityError Severity = "error"

	TriggerOracleAssertion      Trigger = "oracle.assertion"
	TriggerEngineDeterminism    Trigger = "engine.determinism"
	TriggerOracleDuplicate      Trigger = "oracle.duplicate"
	TriggerOracleMissing        Trigger = "oracle.missing"
	TriggerOracleStale          Trigger = "oracle.stale"
	TriggerOracleConstraint     Trigger = "oracle.constraint"
	TriggerEngineRecoverability Trigger = "engine.recoverability"
)

type Rule struct {
	Code      Code      `json:"code"`
	Severity  Severity  `json:"severity"`
	Triggers  []Trigger `json:"triggers"`
	Title     string    `json:"title"`
	Invariant string    `json:"invariant"`
	Reason    string    `json:"reason"`
	Help      []string  `json:"help"`
}

type Diagnostic struct {
	Code      Code     `json:"code"`
	Severity  Severity `json:"severity"`
	Title     string   `json:"title"`
	Observed  string   `json:"observed"`
	Assertion string   `json:"assertion,omitempty"`
	Invariant string   `json:"invariant"`
	Reason    string   `json:"reason"`
	Help      []string `json:"help"`
	Evidence  Evidence `json:"evidence"`
}

type Evidence struct {
	ScheduleRef string `json:"schedule_ref,omitempty"`
	Rows        int    `json:"rows"`
	Trace       string `json:"trace"`
	Observation string `json:"observation,omitempty"`
}

func ValidateCode(code Code) error {
	if !codePattern.MatchString(string(code)) {
		return fmt.Errorf("invalid diagnostic code %q: must match RG followed by three digits", code)
	}
	return nil
}

func ValidateTrigger(trigger Trigger) error {
	switch trigger {
	case TriggerOracleAssertion, TriggerEngineDeterminism,
		TriggerOracleDuplicate, TriggerOracleMissing, TriggerOracleStale,
		TriggerOracleConstraint, TriggerEngineRecoverability:
		return nil
	default:
		return fmt.Errorf("unknown diagnostic trigger %q", trigger)
	}
}

func (rule Rule) Validate() error {
	if err := ValidateCode(rule.Code); err != nil {
		return err
	}
	if rule.Severity != SeverityError {
		return fmt.Errorf("rule %s: severity must be %q", rule.Code, SeverityError)
	}
	if len(rule.Triggers) == 0 {
		return fmt.Errorf("rule %s: at least one trigger is required", rule.Code)
	}
	for index, trigger := range rule.Triggers {
		if err := ValidateTrigger(trigger); err != nil {
			return fmt.Errorf("rule %s trigger[%d]: %w", rule.Code, index, err)
		}
	}
	for name, value := range map[string]string{
		"title": rule.Title, "invariant": rule.Invariant, "reason": rule.Reason,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("rule %s: %s is required", rule.Code, name)
		}
	}
	if len(rule.Help) == 0 {
		return fmt.Errorf("rule %s: at least one help item is required", rule.Code)
	}
	for index, item := range rule.Help {
		if strings.TrimSpace(item) == "" {
			return fmt.Errorf("rule %s: help[%d] is blank", rule.Code, index)
		}
	}
	return nil
}

func TriggerForKind(kind oracle.Kind) (Trigger, bool) {
	switch kind {
	case oracle.KindAssertion:
		return TriggerOracleAssertion, true
	default:
		return "", false
	}
}
