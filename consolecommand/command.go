package consolecommand

import (
	"context"
	"strings"
	"time"

	"emperror.dev/errors"
)

const (
	waitAfterCommand = 800 * time.Millisecond
	logTailLines     = 120
	fetchTimeout     = 6 * time.Second
	readLogTimeout   = 5 * time.Second
)

// Environment exposes the server operations needed to run a console command.
type Environment interface {
	IsRunning(ctx context.Context) (bool, error)
	SendCommand(command string) error
	Readlog(lines int) ([]string, error)
}

// Result is returned after executing a console command and reading recent logs.
type Result struct {
	Command   string   `json:"command"`
	Lines     []string `json:"lines"`
	QueriedAt string   `json:"queried_at"`
}

// Execute sends a command to the server console and returns recent log output.
func Execute(ctx context.Context, env Environment, command string) (*Result, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, errors.New("command is required")
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	running, err := env.IsRunning(ctx)
	if err != nil {
		return nil, err
	}
	if !running {
		return nil, errors.New("server is not running")
	}

	if err := env.SendCommand(command); err != nil {
		return nil, errors.Wrap(err, "failed to send console command")
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(waitAfterCommand):
	}

	lines, err := readLogsWithTimeout(ctx, env, logTailLines)
	if err != nil {
		return nil, err
	}

	return &Result{
		Command:   command,
		Lines:     lines,
		QueriedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func readLogsWithTimeout(ctx context.Context, env Environment, lines int) ([]string, error) {
	type logResult struct {
		lines []string
		err   error
	}

	ch := make(chan logResult, 1)
	go func() {
		out, err := env.Readlog(lines)
		ch <- logResult{lines: out, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, errors.Wrap(ctx.Err(), "timed out reading server logs")
	case result := <-ch:
		if result.err != nil {
			return nil, errors.Wrap(result.err, "failed to read server logs")
		}

		return result.lines, nil
	}
}
