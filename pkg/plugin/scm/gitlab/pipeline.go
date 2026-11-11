package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/unitaryai/osmia/pkg/plugin/scm"
)

// Compile-time check that the backend reports pipeline status.
var _ scm.PipelineStatusProvider = (*GitLabSCMBackend)(nil)

// glPipeline is the subset of a GitLab pipeline we parse.
type glPipeline struct {
	ID        int    `json:"id"`
	SHA       string `json:"sha"`
	Status    string `json:"status"`
	WebURL    string `json:"web_url"`
	CreatedAt string `json:"created_at"`
}

// glJob is the subset of a GitLab CI job we parse.
type glJob struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Stage        string `json:"stage"`
	WebURL       string `json:"web_url"`
	AllowFailure bool   `json:"allow_failure"`
}

// LatestPipeline returns the newest pipeline for the merge request's current
// head commit, with its failed jobs and the tail of each job's log when it
// failed. It returns nil when the head commit has no pipeline yet.
func (b *GitLabSCMBackend) LatestPipeline(ctx context.Context, prURL string) (*scm.Pipeline, error) {
	projectPath, mrIID, err := parseMRURL(prURL)
	if err != nil {
		return nil, fmt.Errorf("parsing merge request URL: %w", err)
	}
	project := fmt.Sprintf("%s/projects/%s", b.baseURL, url.PathEscape(projectPath))

	head, err := b.mergeRequestHead(ctx, project, mrIID)
	if err != nil {
		return nil, err
	}
	latest, err := b.latestPipelineFor(ctx, project, mrIID, head)
	if err != nil || latest == nil {
		return nil, err
	}

	status, err := pipelineStatus(latest.Status)
	if err != nil {
		return nil, err
	}
	pipeline := &scm.Pipeline{
		ID:        strconv.Itoa(latest.ID),
		SHA:       latest.SHA,
		Status:    status,
		URL:       latest.WebURL,
		CreatedAt: parseGitLabTime(latest.CreatedAt),
	}
	if pipeline.Status == scm.PipelineFailed {
		if pipeline.FailedJobs, err = b.failedJobs(ctx, project, latest.ID); err != nil {
			return nil, err
		}
	}
	return pipeline, nil
}

// mergeRequestHead returns the SHA of the merge request's head commit.
func (b *GitLabSCMBackend) mergeRequestHead(ctx context.Context, project string, mrIID int) (string, error) {
	body, err := b.doGet(ctx, fmt.Sprintf("%s/merge_requests/%d", project, mrIID))
	if err != nil {
		return "", fmt.Errorf("fetching merge request: %w", err)
	}
	defer func() { _ = body.Close() }()

	var mr glMR
	if err := json.NewDecoder(body).Decode(&mr); err != nil {
		return "", fmt.Errorf("decoding merge request: %w", err)
	}
	if mr.SHA == "" {
		return "", fmt.Errorf("merge request has no head commit")
	}
	return mr.SHA, nil
}

// latestPipelineFor returns the newest of the merge request's pipelines that
// ran on head, or nil when none has. GitLab lists them newest first, so only
// the first page is read.
func (b *GitLabSCMBackend) latestPipelineFor(ctx context.Context, project string, mrIID int, head string) (*glPipeline, error) {
	body, err := b.doGet(ctx, fmt.Sprintf("%s/merge_requests/%d/pipelines?per_page=20", project, mrIID))
	if err != nil {
		return nil, fmt.Errorf("listing merge request pipelines: %w", err)
	}
	defer func() { _ = body.Close() }()

	var pipelines []glPipeline
	if err := json.NewDecoder(body).Decode(&pipelines); err != nil {
		return nil, fmt.Errorf("decoding merge request pipelines: %w", err)
	}
	for i := range pipelines {
		if pipelines[i].SHA == head {
			return &pipelines[i], nil
		}
	}
	return nil, nil
}

// failedJobs lists the pipeline's failed jobs that are not allowed to fail,
// up to scm.MaxFailedJobs, each with the tail of its log.
func (b *GitLabSCMBackend) failedJobs(ctx context.Context, project string, pipelineID int) ([]scm.FailedJob, error) {
	var jobs []glJob
	err := b.getAllPages(ctx, fmt.Sprintf("%s/pipelines/%d/jobs?scope[]=failed&per_page=100", project, pipelineID),
		func(r io.Reader) error {
			var page []glJob
			if err := json.NewDecoder(r).Decode(&page); err != nil {
				return fmt.Errorf("decoding pipeline jobs: %w", err)
			}
			jobs = append(jobs, page...)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("listing failed pipeline jobs: %w", err)
	}

	var failed []scm.FailedJob
	for _, j := range jobs {
		if j.AllowFailure {
			continue
		}
		if len(failed) == scm.MaxFailedJobs {
			break
		}
		failed = append(failed, scm.FailedJob{
			Name:    j.Name,
			Stage:   j.Stage,
			URL:     j.WebURL,
			LogTail: b.jobLogTail(ctx, project, j.ID),
		})
	}
	return failed, nil
}

// jobLogTail returns the end of a job's log, or "" when it cannot be read.
// A missing log should not stop the follow-up: the job name alone tells the
// agent where to look.
func (b *GitLabSCMBackend) jobLogTail(ctx context.Context, project string, jobID int) string {
	resp, err := b.getJobTrace(ctx, fmt.Sprintf("%s/jobs/%d/trace", project, jobID))
	if err != nil {
		b.logger.Warn("could not fetch job log", "job_id", jobID, "error", err)
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	tail, err := scm.ReadLogTail(resp.Body)
	if err != nil {
		b.logger.Warn("could not read job log", "job_id", jobID, "error", err)
		return ""
	}
	return tail
}

// getJobTrace fetches a job log. GitLab may answer with a redirect to object
// storage. The token is sent in the custom PRIVATE-TOKEN header, which Go's
// client copies onto a redirect to any host, so redirects are not followed
// automatically: a redirect target is fetched as a separate request without
// credentials. The caller must close the body.
func (b *GitLabSCMBackend) getJobTrace(ctx context.Context, traceURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, traceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", b.token)

	noRedirect := *b.client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return nil, err
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return resp, nil
	case http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect:
		_ = resp.Body.Close()
		return b.getUnauthenticated(ctx, resp)
	default:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
}

// getUnauthenticated follows a redirect response with a plain GET that
// carries no credentials.
func (b *GitLabSCMBackend) getUnauthenticated(ctx context.Context, redirect *http.Response) (*http.Response, error) {
	location, err := redirect.Location()
	if err != nil {
		return nil, fmt.Errorf("redirect has no location: %w", err)
	}
	if location.Scheme != "https" && location.Scheme != "http" {
		return nil, fmt.Errorf("redirect to unsupported scheme %q", location.Scheme)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d from redirect target", resp.StatusCode)
	}
	return resp, nil
}

// pipelineStatus maps a GitLab pipeline status to the normalised status.
// The values are those documented for the pipelines API; an unknown one is
// an error rather than a guess, so a new GitLab status is noticed instead of
// silently treated as a failure or a pass.
func pipelineStatus(status string) (scm.PipelineStatus, error) {
	switch status {
	case "created", "waiting_for_resource", "preparing", "pending", "running":
		return scm.PipelineRunning, nil
	case "success":
		return scm.PipelineSucceeded, nil
	case "failed":
		return scm.PipelineFailed, nil
	case "canceled", "canceling", "skipped", "manual", "scheduled":
		return scm.PipelineOther, nil
	default:
		return "", fmt.Errorf("unknown GitLab pipeline status %q", status)
	}
}

// parseGitLabTime parses a GitLab RFC 3339 timestamp, returning the zero
// time when it is missing or malformed.
func parseGitLabTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
