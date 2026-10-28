package automerge

// Closing-link gates (hivecommons/hive#15025). GitHub closes every issue or
// pull request a merged PR names after a closing keyword, in the PR body and
// in the commit messages that land. Two shapes closed the wrong thing:
//
//   - a body that says an issue stays open (`Refs #1023 — partial`) and also
//     `Closes #1023` (projectbluefin/testsuite#1057);
//   - a commit message that reads "Apply the same fix #1036 makes", which
//     closed the open PR #1036 when #1054 squash-merged.
//
// The contradiction gate reads only the body, so every lane runs it early and
// a PR never reaches 090/095. The PR-target gate needs the commit messages and
// one issue read per number, so it runs with the linked-issue gate, for a head
// that is about to merge. Neither keeps state the author has to clear: a body
// edit or a reworded commit lets the next sweep merge.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	gh "github.com/google/go-github/v72/github"
	hgithub "github.com/hivecommons/hive/pkg/github"
	"github.com/hivecommons/hive/pkg/stageengine/prstage"
)

const (
	// closingLinkCommitPages bounds the commit read: GitHub lists at most
	// 250 commits for a pull request.
	closingLinkCommitPages = 3
	// closingLinkCacheMax bounds each closing-link cache; past it the cache
	// starts over rather than growing with every head the hive ever saw.
	closingLinkCacheMax = 4096
)

// closingContradictionGate refuses a PR whose body both closes a repo issue
// and says that issue stays open or is partial
// (`closing-contradiction:#N`). It costs no API call: the lanes run it right
// after their label checks, before admission, CI or soak.
func (c *Engine) closingContradictionGate(owner, repo string, pr *gh.PullRequest) string {
	fullRepo := owner + "/" + repo
	issues := hgithub.ClosingContradictions(pr.GetBody(), fullRepo)
	if len(issues) == 0 {
		return ""
	}
	reason := prstage.ClosingContradictionReason(issues[0])
	c.debug("automerge: PR body closes an issue it says stays open", "repo", fullRepo, "pr", pr.GetNumber(), "issue", issues[0], "reason", reason)
	return reason
}

// closingKeywordPRGate refuses a merge while a closing keyword in the PR body,
// or (with Options.ScanCommitClosingKeywords) in a commit message on the
// head, names another pull request in the same repository
// (`closing-keyword-targets-pr:#N`). A pull request is never the intended
// target of a closing keyword. Whether a number is a pull request never
// changes, so each number is read at most once per process, and the commit
// messages once per head. A read error other than 404/410 fails closed with
// linkedIssueReasonCheck.
func (c *Engine) closingKeywordPRGate(ctx context.Context, owner, repo string, pr *gh.PullRequest) (string, error) {
	fullRepo := owner + "/" + repo
	targets := hgithub.GitHubClosingRefs(pr.GetBody(), fullRepo)
	if c.scanCommitClosingKeywords {
		fromCommits, err := c.commitClosingRefs(ctx, owner, repo, pr)
		if err != nil {
			return linkedIssueReasonCheck, err
		}
		targets = append(targets, fromCommits...)
	}
	seen := map[int]bool{}
	for _, n := range targets {
		if n == pr.GetNumber() || seen[n] {
			continue
		}
		seen[n] = true
		isPR, err := c.numberIsPullRequest(ctx, owner, repo, n)
		if err != nil {
			return linkedIssueReasonCheck, err
		}
		if isPR {
			reason := prstage.ClosingKeywordTargetsPRReason(n)
			c.info("automerge: closing keyword targets a pull request", "repo", fullRepo, "pr", pr.GetNumber(), "target", n, "reason", reason)
			return reason, nil
		}
	}
	return "", nil
}

// noteClosingTargetKind records whether repo#n is a pull request, from an
// issue read the caller already made (the linked-issue gate reads every
// issue the body closes), so closingKeywordPRGate needs no second GET.
func (c *Engine) noteClosingTargetKind(owner, repo string, n int, isPR bool) {
	key := strings.ToLower(owner+"/"+repo) + "#" + strconv.Itoa(n)
	c.closingLinkMu.Lock()
	defer c.closingLinkMu.Unlock()
	if c.closingTargetKinds == nil || len(c.closingTargetKinds) >= closingLinkCacheMax {
		c.closingTargetKinds = map[string]bool{}
	}
	c.closingTargetKinds[key] = isPR
}

// numberIsPullRequest reports whether repo#n is a pull request. A number
// GitHub reports missing (404/410) is not one.
func (c *Engine) numberIsPullRequest(ctx context.Context, owner, repo string, n int) (bool, error) {
	key := strings.ToLower(owner+"/"+repo) + "#" + strconv.Itoa(n)
	c.closingLinkMu.Lock()
	isPR, ok := c.closingTargetKinds[key]
	c.closingLinkMu.Unlock()
	if ok {
		return isPR, nil
	}
	issue, _, err := c.gh.Issues.Get(hgithub.WithRESTCaller(ctx, "hive:automerge_closing_link"), owner, repo, n)
	if err != nil {
		if isGitHubStatus(err, http.StatusNotFound) || isGitHubStatus(err, http.StatusGone) {
			c.noteClosingTargetKind(owner, repo, n, false)
			return false, nil
		}
		return false, fmt.Errorf("reading closing-keyword target %s/%s#%d: %w", owner, repo, n, err)
	}
	isPR = issue.IsPullRequest()
	c.noteClosingTargetKind(owner, repo, n, isPR)
	return isPR, nil
}

// commitClosingRefs returns the repo numbers the commit messages on the PR's
// head close the way GitHub does. With a squash merge GitHub's default
// message is built from these messages, and with a merge or rebase they land
// on the base as they are, so every method carries them.
func (c *Engine) commitClosingRefs(ctx context.Context, owner, repo string, pr *gh.PullRequest) ([]int, error) {
	fullRepo := owner + "/" + repo
	key := strings.ToLower(fullRepo) + "#" + strconv.Itoa(pr.GetNumber()) + "@" + pr.GetHead().GetSHA()
	c.closingLinkMu.Lock()
	refs, ok := c.closingCommitRefs[key]
	c.closingLinkMu.Unlock()
	if ok {
		return refs, nil
	}
	caller := hgithub.WithRESTCaller(ctx, "hive:automerge_closing_link")
	opts := &gh.ListOptions{PerPage: 100}
	var messages []string
	for page := 0; page < closingLinkCommitPages; page++ {
		commits, resp, err := c.gh.PullRequests.ListCommits(caller, owner, repo, pr.GetNumber(), opts)
		if err != nil {
			return nil, fmt.Errorf("listing commits for %s#%d: %w", fullRepo, pr.GetNumber(), err)
		}
		for _, commit := range commits {
			messages = append(messages, commit.GetCommit().GetMessage())
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	refs = hgithub.GitHubClosingRefs(strings.Join(messages, "\n\n"), fullRepo)
	c.closingLinkMu.Lock()
	if c.closingCommitRefs == nil || len(c.closingCommitRefs) >= closingLinkCacheMax {
		c.closingCommitRefs = map[string][]int{}
	}
	c.closingCommitRefs[key] = refs
	c.closingLinkMu.Unlock()
	return refs, nil
}
