package scm

import (
	"context"
	"io"
	"regexp"
	"time"
)

// PipelineStatus is the outcome of a pull request's CI run, normalised
// across SCM providers.
type PipelineStatus string

const (
	// PipelineRunning means at least one job has not finished yet.
	PipelineRunning PipelineStatus = "running"
	// PipelineSucceeded means every job passed or was allowed to fail.
	PipelineSucceeded PipelineStatus = "succeeded"
	// PipelineFailed means at least one required job failed or timed out.
	PipelineFailed PipelineStatus = "failed"
	// PipelineOther covers outcomes nobody should act on: cancelled, skipped,
	// or waiting for a manual action.
	PipelineOther PipelineStatus = "other"
)

// MaxFailedJobs bounds how many failed jobs a backend reports for one
// pipeline, and so how many job logs it downloads.
const MaxFailedJobs = 5

// MaxLogTailBytes bounds how much of each failed job's log a backend keeps.
// The end of a log is where the failure is reported, and the tail is copied
// into a follow-up task's prompt.
const MaxLogTailBytes = 8 << 10

// maxLogReadBytes bounds how much of a job log is read while looking for its
// tail, so a runaway log cannot hold a poll up for long.
const maxLogReadBytes = 32 << 20

// Pipeline is the latest CI run on a pull request's current head commit.
type Pipeline struct {
	// ID identifies the run. It changes whenever CI runs again, so callers
	// use it to act on each failed run once.
	ID string
	// SHA is the commit the run tested.
	SHA string
	// Status is the normalised outcome.
	Status PipelineStatus
	// URL links to the run in the provider's UI. May be empty.
	URL string
	// CreatedAt is when the run started.
	CreatedAt time.Time
	// FailedJobs lists up to MaxFailedJobs failed jobs. Set only when
	// Status is PipelineFailed.
	FailedJobs []FailedJob
}

// FailedJob is one failed job in a pipeline.
type FailedJob struct {
	// Name is the job name.
	Name string
	// Stage is the pipeline stage, where the provider has one.
	Stage string
	// URL links to the job in the provider's UI. May be empty.
	URL string
	// LogTail is the end of the job's log, at most MaxLogTailBytes, with
	// terminal colour codes removed. May be empty when the log could not be
	// read.
	LogTail string
}

// PipelineStatusProvider is an optional capability for SCM backends that
// can report the CI status of a pull request.
//
// The review poller uses it to start a follow-up job when a pipeline on a
// pull request Osmia opened fails. Like IdentityProvider it is separate from
// Backend: callers type-assert for it, so gRPC plugin backends keep working
// unchanged and the plugin wire protocol does not move.
type PipelineStatusProvider interface {
	// LatestPipeline returns the newest pipeline for the pull request's
	// current head commit, or nil when that commit has no pipeline yet. A
	// pipeline for an older commit is never returned, because a newer push
	// has already superseded it.
	LatestPipeline(ctx context.Context, prURL string) (*Pipeline, error)
}

// ansiEscape matches terminal colour and cursor escape sequences, which CI
// logs are full of and which only add noise to a prompt.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*\x07`)

// ReadLogTail reads r and returns its last MaxLogTailBytes, with terminal
// escape codes removed. It reads at most maxLogReadBytes; a longer log is
// cut there, which keeps a slow or endless log from holding a poll up.
func ReadLogTail(r io.Reader) (string, error) {
	tail := make([]byte, 0, MaxLogTailBytes)
	buf := make([]byte, 32<<10)
	limited := io.LimitReader(r, maxLogReadBytes)
	for {
		n, err := limited.Read(buf)
		tail = append(tail, buf[:n]...)
		if len(tail) > MaxLogTailBytes {
			tail = append(tail[:0], tail[len(tail)-MaxLogTailBytes:]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return ansiEscape.ReplaceAllString(string(tail), ""), nil
}
