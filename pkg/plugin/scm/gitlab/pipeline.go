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
	FinishedAt   string `json:"finished_at"`
}

// LatestPipeline returns the merge request's head pipeline, with its failed
// jobs and the tail of each job's log when it failed. It returns nil when the
// head commit has no pipeline yet.
//
// The head pipeline is read from the merge request itself rather than by
// matching pipelines to the head SHA: a merged-results pipeline tests a
// temporary merge commit, so its SHA never equals the source branch head.
func (b *GitLabSCMBackend) LatestPipeline(ctx context.Context, prURL string) (*scm.Pipeline, error) {
	projectPath, mrIID, err := parseMRURL(prURL)
	if err != nil {
		return nil, fmt.Errorf("parsing merge request URL: %w", err)
	}
	project := fmt.Sprintf("%s/projects/%s", b.baseURL, url.PathEscape(projectPath))

	head, err := b.headPipeline(ctx, project, mrIID)
	if err != nil || head == nil {
		return nil, err
	}

	status, err := pipelineStatus(head.Status)
	if err != nil {
		return nil, err
	}
	pipeline := &scm.Pipeline{
		ID:        strconv.Itoa(head.ID),
		SHA:       head.SHA,
		Status:    status,
		URL:       head.WebURL,
		CreatedAt: parseGitLabTime(head.CreatedAt),
	}
	if pipeline.Status == scm.PipelineFailed {
		if err := b.addFailedJobs(ctx, project, head.ID, pipeline); err != nil {
			return nil, err
		}
	}
	return pipeline, nil
}

// headPipeline returns the merge request's head pipeline, or nil when it
// has none.
func (b *GitLabSCMBackend) headPipeline(ctx context.Context, project string, mrIID int) (*glPipeline, error) {
	body, err := b.doGet(ctx, fmt.Sprintf("%s/merge_requests/%d", project, mrIID))
	if err != nil {
		return nil, fmt.Errorf("fetching merge request: %w", err)
	}
	defer func() { _ = body.Close() }()

	var mr glMR
	if err := json.NewDecoder(body).Decode(&mr); err != nil {
		return nil, fmt.Errorf("decoding merge request: %w", err)
	}
	return mr.HeadPipeline, nil
}

// addFailedJobs fills in the pipeline's failed jobs that are not allowed to
// fail, up to scm.MaxFailedJobs, each with the tail of its log. It also sets
// FailedAt, and extends the ID with the newest failed job's ID: retrying
// failed jobs keeps the pipeline ID but gives the retried jobs new IDs, so
// each round of failures gets its own identity.
func (b *GitLabSCMBackend) addFailedJobs(ctx context.Context, project string, pipelineID int, pipeline *scm.Pipeline) error {
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
		return fmt.Errorf("listing failed pipeline jobs: %w", err)
	}

	newestJobID := 0
	for _, j := range jobs {
		if j.AllowFailure {
			continue
		}
		newestJobID = max(newestJobID, j.ID)
		if finished := parseGitLabTime(j.FinishedAt); finished.After(pipeline.FailedAt) {
			pipeline.FailedAt = finished
		}
		if len(pipeline.FailedJobs) == scm.MaxFailedJobs {
			continue
		}
		pipeline.FailedJobs = append(pipeline.FailedJobs, scm.FailedJob{
			Name:    j.Name,
			Stage:   j.Stage,
			URL:     j.WebURL,
			LogTail: b.jobLogTail(ctx, project, j.ID),
		})
	}
	if newestJobID > 0 {
		pipeline.ID += ":" + strconv.Itoa(newestJobID)
	}
	return nil
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
