// Package config loads and validates the weavegate run configuration file.
package config

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var assertionIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// SupportedAdapter is the only sut.adapter value accepted today.
const SupportedAdapter = "gonative"

// Default run values applied when the corresponding key is omitted.
const (
	DefaultRepeat          = 20
	DefaultArriveTimeoutMS = 3000
	DefaultExplorePasses   = 3
)

// Config is the validated, decoded form of .weavegate/config.yaml.
type Config struct {
	Target    Target              `yaml:"target"`
	Scenarios map[string]Scenario `yaml:"scenarios"`
	Oracle    Oracle              `yaml:"oracle"`
	Run       Run                 `yaml:"run"`
}

// Target names the database image, schema sources, and SUT under test.
type Target struct {
	DB     string `yaml:"db"`
	Schema Schema `yaml:"schema"`
	SUT    SUT    `yaml:"sut"`
}

// Schema locates the synthetic migration and seed SQL sources.
type Schema struct {
	Migrations string `yaml:"migrations"`
	Seed       string `yaml:"seed"`
}

// SUT selects the adapter, built-in entrypoint, and variant under test.
type SUT struct {
	Adapter    string `yaml:"adapter"`
	Entrypoint string `yaml:"entrypoint"`
	Variant    string `yaml:"variant"`
}

// Scenario names one scenario's workers and required sync-point order.
type Scenario struct {
	Workers    []Worker `yaml:"workers"`
	SyncPoints []string `yaml:"sync_points"`
}

// Worker binds a stable worker ID to a command and its parameters. Every
// worker in a scenario must declare the same Args (A-2).
type Worker struct {
	ID      string            `yaml:"id"`
	Command string            `yaml:"command"`
	Args    map[string]string `yaml:"args"`
}

// Oracle declares the invariant assertions judged after every run.
type Oracle struct {
	Assertions []Assertion `yaml:"assertions"`
}

// Assertion is one zero-row SQL invariant.
type Assertion struct {
	ID         string `yaml:"id"`
	SQL        string `yaml:"sql"`
	ExpectRows int    `yaml:"expect_rows"`
}

// Run holds the repeat, timeout, and exploration budget.
type Run struct {
	Repeat          int `yaml:"repeat"`
	ArriveTimeoutMS int `yaml:"arrive_timeout_ms"`
	ExplorePasses   int `yaml:"explore_passes"`
}

// Validate rejects an incomplete or ambiguous configuration before any
// container starts.
func (c Config) Validate() error {
	if err := c.Target.validate(); err != nil {
		return err
	}

	if len(c.Scenarios) == 0 {
		return errors.New("scenarios: at least one scenario is required")
	}
	names := make([]string, 0, len(c.Scenarios))
	for name := range c.Scenarios {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return errors.New("scenarios: scenario name is blank")
		}
		if err := c.Scenarios[name].validate(name); err != nil {
			return err
		}
	}

	if err := c.Oracle.validate(); err != nil {
		return err
	}

	return c.Run.validate()
}

func (t Target) validate() error {
	if strings.TrimSpace(t.DB) == "" {
		return errors.New("target.db is required")
	}
	if !strings.HasPrefix(t.DB, "mysql:") {
		return fmt.Errorf("target.db %q must have prefix \"mysql:\" (see docs/reference/config.md)", t.DB)
	}
	if strings.TrimSpace(t.Schema.Migrations) == "" {
		return errors.New("target.schema.migrations is required")
	}
	if strings.TrimSpace(t.Schema.Seed) == "" {
		return errors.New("target.schema.seed is required")
	}
	if strings.TrimSpace(t.SUT.Adapter) == "" {
		return errors.New("target.sut.adapter is required")
	}
	if t.SUT.Adapter != SupportedAdapter {
		return fmt.Errorf(
			"target.sut.adapter %q is not supported; supported adapters: %s",
			t.SUT.Adapter,
			SupportedAdapter,
		)
	}
	if strings.TrimSpace(t.SUT.Entrypoint) == "" {
		return errors.New("target.sut.entrypoint is required")
	}
	if strings.ContainsAny(t.SUT.Entrypoint, "/.") {
		return fmt.Errorf(
			"target.sut.entrypoint %q is a built-in ID, not a path; see docs/reference/config.md for known IDs",
			t.SUT.Entrypoint,
		)
	}
	if strings.TrimSpace(t.SUT.Variant) == "" {
		return errors.New("target.sut.variant is required")
	}
	return nil
}

func (s Scenario) validate(name string) error {
	if len(s.Workers) == 0 {
		return fmt.Errorf("scenarios[%q]: at least one worker is required", name)
	}
	if len(s.SyncPoints) == 0 {
		return fmt.Errorf("scenarios[%q]: at least one sync-point is required", name)
	}

	seenWorkers := make(map[string]struct{}, len(s.Workers))
	var referenceArgs map[string]string
	var referenceWorkerID string
	for index, worker := range s.Workers {
		if strings.TrimSpace(worker.ID) == "" {
			return fmt.Errorf("scenarios[%q] workers[%d]: id is required", name, index)
		}
		if strings.TrimSpace(worker.Command) == "" {
			return fmt.Errorf("scenarios[%q] workers[%d] %q: command is required", name, index, worker.ID)
		}
		if _, exists := seenWorkers[worker.ID]; exists {
			return fmt.Errorf("scenarios[%q] workers[%d]: duplicate worker id %q", name, index, worker.ID)
		}
		seenWorkers[worker.ID] = struct{}{}

		if index == 0 {
			referenceArgs = worker.Args
			referenceWorkerID = worker.ID
			continue
		}
		if !stringMapEqual(worker.Args, referenceArgs) {
			return fmt.Errorf(
				"scenarios[%q] worker %q: args must match worker %q's args",
				name,
				worker.ID,
				referenceWorkerID,
			)
		}
	}

	seenPoints := make(map[string]struct{}, len(s.SyncPoints))
	for index, point := range s.SyncPoints {
		if strings.TrimSpace(point) == "" {
			return fmt.Errorf("scenarios[%q] sync_points[%d]: name is blank", name, index)
		}
		if _, exists := seenPoints[point]; exists {
			return fmt.Errorf("scenarios[%q] sync_points[%d]: duplicate sync-point %q", name, index, point)
		}
		seenPoints[point] = struct{}{}
	}

	return nil
}

func (o Oracle) validate() error {
	if len(o.Assertions) == 0 {
		return errors.New("oracle.assertions: at least one assertion is required")
	}

	seen := make(map[string]struct{}, len(o.Assertions))
	for index, assertion := range o.Assertions {
		if !assertionIDPattern.MatchString(assertion.ID) {
			return fmt.Errorf(
				"oracle.assertions[%d]: invalid id %q: must match %s",
				index,
				assertion.ID,
				assertionIDPattern.String(),
			)
		}
		if _, exists := seen[assertion.ID]; exists {
			return fmt.Errorf("oracle.assertions[%d]: duplicate assertion id %q", index, assertion.ID)
		}
		seen[assertion.ID] = struct{}{}

		if strings.TrimSpace(assertion.SQL) == "" {
			return fmt.Errorf("oracle.assertions[%d] %q: sql is required", index, assertion.ID)
		}
		if assertion.ExpectRows != 0 {
			return fmt.Errorf(
				"oracle.assertions[%d] %q: expect_rows must be 0, got %d",
				index,
				assertion.ID,
				assertion.ExpectRows,
			)
		}
	}
	return nil
}

func (r Run) validate() error {
	if r.Repeat < 1 {
		return fmt.Errorf("run.repeat must be positive, got %d", r.Repeat)
	}
	if r.ArriveTimeoutMS < 1 {
		return fmt.Errorf("run.arrive_timeout_ms must be positive, got %d", r.ArriveTimeoutMS)
	}
	if r.ExplorePasses < 1 {
		return fmt.Errorf("run.explore_passes must be positive, got %d", r.ExplorePasses)
	}
	return nil
}

func stringMapEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
