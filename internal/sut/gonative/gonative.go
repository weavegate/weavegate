// Package gonative runs Go-native SUT commands on worker-owned DB connections.
package gonative

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/weavegate/weavegate/internal/fixture"
	"github.com/weavegate/weavegate/internal/sut"
)

// CommandFunc executes one named worker command on its dedicated connection.
type CommandFunc func(ctx context.Context, workerID string, conn *sql.Conn) error

// Registry resolves the commands available for one SUT configuration.
type Registry interface {
	Commands(cfg sut.SUTConfig) (map[string]CommandFunc, error)
}

type adapterState uint8

const (
	adapterStateNew adapterState = iota
	adapterStateStarted
	adapterStateStopped
)

type activeWorker struct {
	id         uint64
	workerID   string
	command    string
	cancel     context.CancelFunc
	results    chan sut.WorkerResult
	done       chan struct{}
	cleanupErr error
}

type adapter struct {
	mu sync.Mutex

	registry Registry
	state    adapterState
	db       *fixture.DB
	commands map[string]CommandFunc
	active   map[uint64]*activeWorker
	nextID   uint64
}

var (
	_ sut.Adapter = (*adapter)(nil)
	_ sut.Handle  = (*adapter)(nil)
)

// New returns a Go-native adapter backed by reg.
func New(reg Registry) sut.Adapter {
	return &adapter{
		registry: reg,
		active:   make(map[uint64]*activeWorker),
	}
}

func (a *adapter) Start(
	ctx context.Context,
	cfg sut.SUTConfig,
	db *fixture.DB,
) (sut.Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start Go-native SUT adapter: %w", err)
	}
	if db == nil || db.SQL == nil {
		return nil, fmt.Errorf("start Go-native SUT adapter: database is required")
	}
	if a.registry == nil {
		return nil, fmt.Errorf("start Go-native SUT adapter: command registry is required")
	}

	a.mu.Lock()
	if a.state != adapterStateNew {
		state := a.state
		a.mu.Unlock()
		return nil, fmt.Errorf("start Go-native SUT adapter: invalid state %s", state)
	}
	a.mu.Unlock()

	registered, err := a.registry.Commands(cfg)
	if err != nil {
		return nil, fmt.Errorf("start Go-native SUT adapter: resolve commands: %w", err)
	}
	commands, err := copyCommands(registered)
	if err != nil {
		return nil, fmt.Errorf("start Go-native SUT adapter: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start Go-native SUT adapter: %w", err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != adapterStateNew {
		return nil, fmt.Errorf("start Go-native SUT adapter: invalid state %s", a.state)
	}

	a.db = db
	a.commands = commands
	a.state = adapterStateStarted
	return a, nil
}

func (a *adapter) Invoke(
	ctx context.Context,
	workerID string,
	commandName string,
) (<-chan sut.WorkerResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("invoke worker %q command %q: %w", workerID, commandName, err)
	}
	if strings.TrimSpace(workerID) == "" {
		return nil, fmt.Errorf("invoke command %q: worker ID is required", commandName)
	}
	if strings.TrimSpace(commandName) == "" {
		return nil, fmt.Errorf("invoke worker %q: command is required", workerID)
	}

	a.mu.Lock()
	if a.state != adapterStateStarted {
		state := a.state
		a.mu.Unlock()
		return nil, fmt.Errorf(
			"invoke worker %q command %q: invalid adapter state %s",
			workerID,
			commandName,
			state,
		)
	}
	command, ok := a.commands[commandName]
	if !ok {
		a.mu.Unlock()
		return nil, fmt.Errorf(
			"invoke worker %q command %q: command is not registered",
			workerID,
			commandName,
		)
	}
	if a.db == nil || a.db.SQL == nil {
		a.mu.Unlock()
		return nil, fmt.Errorf(
			"invoke worker %q command %q: database is unavailable",
			workerID,
			commandName,
		)
	}

	workerCtx, cancel := context.WithCancel(ctx)
	a.nextID++
	worker := &activeWorker{
		id:       a.nextID,
		workerID: workerID,
		command:  commandName,
		cancel:   cancel,
		results:  make(chan sut.WorkerResult, 1),
		done:     make(chan struct{}),
	}
	a.active[worker.id] = worker
	db := a.db.SQL
	a.mu.Unlock()

	conn, err := db.Conn(workerCtx)
	if err != nil {
		a.completeUnstartedWorker(worker, nil)
		return nil, fmt.Errorf(
			"invoke worker %q command %q: acquire connection: %w",
			workerID,
			commandName,
			err,
		)
	}
	if err := workerCtx.Err(); err != nil {
		closeErr := closeWorkerConnection(workerID, commandName, conn)
		a.completeUnstartedWorker(worker, closeErr)
		return nil, errors.Join(
			fmt.Errorf("invoke worker %q command %q: %w", workerID, commandName, err),
			closeErr,
		)
	}

	go a.runWorker(workerCtx, worker, command, conn)
	return worker.results, nil
}

func (a *adapter) Stop(ctx context.Context) error {
	a.mu.Lock()
	if a.state != adapterStateStopped {
		a.state = adapterStateStopped
	}
	workers := make([]*activeWorker, 0, len(a.active))
	for _, worker := range a.active {
		workers = append(workers, worker)
	}
	a.mu.Unlock()

	for _, worker := range workers {
		worker.cancel()
	}

	var stopErr error
	for _, worker := range workers {
		select {
		case <-worker.done:
			stopErr = errors.Join(stopErr, worker.cleanupErr)
		case <-ctx.Done():
			return errors.Join(
				stopErr,
				fmt.Errorf("stop Go-native SUT adapter: %w", ctx.Err()),
			)
		}
	}

	return stopErr
}

func (a *adapter) runWorker(
	ctx context.Context,
	worker *activeWorker,
	command CommandFunc,
	conn *sql.Conn,
) {
	startedAt := time.Now()
	commandErr := command(ctx, worker.workerID, conn)
	closeErr := closeWorkerConnection(worker.workerID, worker.command, conn)
	result := sut.WorkerResult{
		WorkerID: worker.workerID,
		Err:      errors.Join(commandErr, closeErr),
		Duration: time.Since(startedAt),
	}

	worker.cancel()
	a.mu.Lock()
	delete(a.active, worker.id)
	worker.cleanupErr = closeErr
	worker.results <- result
	close(worker.results)
	close(worker.done)
	a.mu.Unlock()
}

func (a *adapter) completeUnstartedWorker(worker *activeWorker, cleanupErr error) {
	worker.cancel()
	a.mu.Lock()
	delete(a.active, worker.id)
	worker.cleanupErr = cleanupErr
	close(worker.results)
	close(worker.done)
	a.mu.Unlock()
}

func copyCommands(registered map[string]CommandFunc) (map[string]CommandFunc, error) {
	commands := make(map[string]CommandFunc, len(registered))
	for name, command := range registered {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("registered command name is empty")
		}
		if command == nil {
			return nil, fmt.Errorf("registered command %q is nil", name)
		}
		commands[name] = command
	}

	return commands, nil
}

func closeWorkerConnection(workerID, command string, conn *sql.Conn) error {
	if conn == nil {
		return nil
	}
	if err := conn.Close(); err != nil {
		return fmt.Errorf(
			"worker %q command %q: close connection: %w",
			workerID,
			command,
			err,
		)
	}

	return nil
}

func (s adapterState) String() string {
	switch s {
	case adapterStateNew:
		return "not-started"
	case adapterStateStarted:
		return "started"
	case adapterStateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}
