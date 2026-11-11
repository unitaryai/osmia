package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/unitaryai/osmia/pkg/plugin/scm"
)

// Compile-time check that the backend reports pipeline status.
var _ scm.PipelineStatusProvider = (*GitHubSCMBackend)(nil)

// githubActionsApp is the app slug of check runs created by GitHub Actions,
// whose job logs can be downloaded.
const githubActionsApp = "github-actions"

// ghCheckRun is the subset of a GitHub check run we parse.
type ghCheckRun struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	HTMLURL     string `json:"html_url"`
	DetailsURL  string `json:"details_url"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	App         struct {
		Slug string `json:"slug"`
	} `json:"app"`
	Output struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Text    string `json:"text"`
	} `json:"output"`
}

// LatestPipeline returns the CI status of the pull request's current head
// commit, built from that commit's check runs, with the failed runs and the
// tail of each one's log when any failed. It returns nil when the head commit
// has no check runs yet.
//
// GitHub has no single pipeline object, so the pipeline ID is the head SHA
// plus the newest check run ID: re-running checks on the same commit creates
// new check runs and so a new ID.
func (b *GitHubSCMBackend) LatestPipeline(ctx context.Context, prURL string) (*scm.Pipeline, error) {
	owner, repo, number, err := parsePRURL(prURL)
	if err != nil {
		return nil, fmt.Errorf("parsing pull request URL: %w", err)
	}

	head, err := b.pullRequestHead(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}
	runs, err := b.checkRuns(ctx, owner, repo, head)
	if err != nil || len(runs) == 0 {
		return nil, err
	}

	status, err := checkRunsStatus(runs)
	if err != nil {
		return nil, err
	}
	pipeline := &scm.Pipeline{
		ID:        head + ":" + strconv.FormatInt(newestCheckRunID(runs), 10),
		SHA:       head,
		Status:    status,
		CreatedAt: earliestStart(runs),
	}
	if status == scm.PipelineFailed {
		pipeline.FailedJobs = b.failedCheckRuns(ctx, owner, repo, runs)
		pipeline.FailedAt = latestFailure(runs)
	}
	return pipeline, nil
}

// pullRequestHead returns the SHA of the pull request's head commit.
func (b *GitHubSCMBackend) pullRequestHead(ctx context.Context, owner, repo string, number int) (string, error) {
	body, err := b.doGet(ctx, fmt.Sprintf("%s/repos/%s/%s/pulls/%d", b.baseURL, owner, repo, number))
	if err != nil {
		return "", fmt.Errorf("fetching pull request: %w", err)
	}
	defer func() { _ = body.Close() }()

	var pr ghPR
	if err := json.NewDecoder(body).Decode(&pr); err != nil {
		return "", fmt.Errorf("decoding pull request: %w", err)
	}
	if pr.Head.SHA == "" {
		return "", fmt.Errorf("pull request has no head commit")
	}
	return pr.Head.SHA, nil
}

// checkRuns lists every check run on a commit.
func (b *GitHubSCMBackend) checkRuns(ctx context.Context, owner, repo, sha string) ([]ghCheckRun, error) {
	var runs []ghCheckRun
	err := b.getAllPages(ctx, fmt.Sprintf("%s/repos/%s/%s/commits/%s/check-runs?per_page=100", b.baseURL, owner, repo, sha),
		func(r io.Reader) error {
			var page struct {
				CheckRuns []ghCheckRun `json:"check_runs"`
			}
			if err := json.NewDecoder(r).Decode(&page); err != nil {
				return fmt.Errorf("decoding check runs: %w", err)
			}
			runs = append(runs, page.CheckRuns...)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing check runs: %w", err)
	}
	return runs, nil
}

// failedCheckRuns returns up to scm.MaxFailedJobs failed check runs, each
// with the tail of its log.
func (b *GitHubSCMBackend) failedCheckRuns(ctx context.Context, owner, repo string, runs []ghCheckRun) []scm.FailedJob {
	var failed []scm.FailedJob
	for _, run := range runs {
		if !isFailedConclusion(run.Conclusion) {
			continue
		}
		if len(failed) == scm.MaxFailedJobs {
			break
		}
		failed = append(failed, scm.FailedJob{
			Name:    run.Name,
			URL:     run.HTMLURL,
			LogTail: b.checkRunLogTail(ctx, owner, repo, run),
		})
	}
	return failed
}

// checkRunLogTail returns the end of a check run's log. Logs can only be
// downloaded for GitHub Actions jobs; for checks from other apps, and when
// the download fails, it falls back to the check run's own output text.
func (b *GitHubSCMBackend) checkRunLogTail(ctx context.Context, owner, repo string, run ghCheckRun) string {
	if run.App.Slug == githubActionsApp {
		if tail, ok := b.actionsJobLogTail(ctx, owner, repo, actionsJobID(run)); ok {
			return tail
		}
	}
	output := strings.TrimSpace(strings.Join([]string{run.Output.Title, run.Output.Summary, run.Output.Text}, "\n\n"))
	tail, err := scm.ReadLogTail(strings.NewReader(output))
	if err != nil {
		return ""
	}
	return tail
}

// actionsJobLogTail downloads the tail of a GitHub Actions job log.
//
// GitHub answers with a redirect to a short-lived signed URL on another host.
// The redirect is not followed automatically: Go's client keeps the
// Authorization header when the new host matches closely enough (the same
// host on another port, for example), and the signed URL needs no token
// anyway. The download is made as a separate request without credentials.
func (b *GitHubSCMBackend) actionsJobLogTail(ctx context.Context, owner, repo string, jobID int64) (string, bool) {
	location, err := b.actionsJobLogURL(ctx, owner, repo, jobID)
	if err != nil {
		b.logger.Warn("could not fetch job log", "job_id", jobID, "error", err)
		return "", false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return "", false
	}
	resp, err := b.client.Do(req)
	if err != nil {
		b.logger.Warn("could not download job log", "job_id", jobID, "error", err)
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b.logger.Warn("could not download job log", "job_id", jobID, "status", resp.StatusCode)
		return "", false
	}
	tail, err := scm.ReadLogTail(resp.Body)
	if err != nil {
		b.logger.Warn("could not read job log", "job_id", jobID, "error", err)
		return "", false
	}
	return tail, true
}

// actionsJobLogURL asks GitHub where a job's log can be downloaded from and
// returns the signed URL from its redirect.
func (b *GitHubSCMBackend) actionsJobLogURL(ctx context.Context, owner, repo string, jobID int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s/actions/jobs/%d/logs", b.baseURL, owner, repo, jobID), nil)
	if err != nil {
		return "", err
	}
	b.setAuthHeaders(req)

	noRedirect := *b.client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	location, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("log redirect has no location: %w", err)
	}
	if location.Scheme != "https" && location.Scheme != "http" {
		return "", fmt.Errorf("log redirect to unsupported scheme %q", location.Scheme)
	}
	return location.String(), nil
}

// checkRunsStatus combines a commit's check runs into one status: running
// while any run is unfinished, failed when any finished run failed, and
// succeeded only when every run passed or was neutral or skipped.
//
// The status and conclusion values are those documented for the check runs
// API. An unknown one is an error rather than a guess, so a new GitHub value
// is noticed instead of being silently treated as a pass or a failure.
func checkRunsStatus(runs []ghCheckRun) (scm.PipelineStatus, error) {
	running, failed, other := false, false, false
	for _, run := range runs {
		switch run.Status {
		case "queued", "in_progress", "waiting", "requested", "pending":
			running = true
			continue
		case "completed":
		default:
			return "", fmt.Errorf("unknown GitHub check run status %q", run.Status)
		}
		switch run.Conclusion {
		case "success", "neutral", "skipped":
		case "failure", "timed_out", "startup_failure":
			failed = true
		case "cancelled", "action_required", "stale":
			other = true
		default:
			return "", fmt.Errorf("unknown GitHub check run conclusion %q", run.Conclusion)
		}
	}
	switch {
	case running:
		return scm.PipelineRunning, nil
	case failed:
		return scm.PipelineFailed, nil
	case other:
		return scm.PipelineOther, nil
	default:
		return scm.PipelineSucceeded, nil
	}
}

// isFailedConclusion reports whether a check run conclusion is a failure.
func isFailedConclusion(conclusion string) bool {
	return conclusion == "failure" || conclusion == "timed_out" || conclusion == "startup_failure"
}

// newestCheckRunID returns the highest check run ID.
func newestCheckRunID(runs []ghCheckRun) int64 {
	var newest int64
	for _, run := range runs {
		newest = max(newest, run.ID)
	}
	return newest
}

// actionsJobPattern matches the job ID at the end of a GitHub Actions check
// run's details URL (.../actions/runs/<run>/job/<job>).
var actionsJobPattern = regexp.MustCompile(`/actions/runs/\d+/job/(\d+)`)

// actionsJobID returns the Actions job ID behind a check run. GitHub does not
// promise that the job ID and the check run ID are the same, so the job ID is
// read from the details URL, falling back to the check run ID.
func actionsJobID(run ghCheckRun) int64 {
	if m := actionsJobPattern.FindStringSubmatch(run.DetailsURL); m != nil {
		if id, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return id
		}
	}
	return run.ID
}

// latestFailure returns when the most recently finished failed check run
// completed, or the zero time.
func latestFailure(runs []ghCheckRun) time.Time {
	var latest time.Time
	for _, run := range runs {
		if !isFailedConclusion(run.Conclusion) {
			continue
		}
		if t, err := time.Parse(time.RFC3339, run.CompletedAt); err == nil && t.After(latest) {
			latest = t
		}
	}
	return latest
}

// earliestStart returns when the first check run started, or the zero time.
func earliestStart(runs []ghCheckRun) time.Time {
	var earliest time.Time
	for _, run := range runs {
		t, err := time.Parse(time.RFC3339, run.StartedAt)
		if err != nil {
			continue
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}
