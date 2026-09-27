package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/hivecommons/hive/pkg/persona"
)

func TestRouteMessage_RedeliveredMessageIDRunsOnce(t *testing.T) {
	s := NewService(&recordingBackend{}, Config{AllowedUsers: []string{"uid:owner"}}, discardLogger())
	calls := 0
	s.RegisterCommand("ping", func(context.Context, string) (string, error) {
		calls++
		return "", nil
	})
	s.routeMessage(context.Background(), makeMsg("msg-B", "!ping", false))
	s.routeMessage(context.Background(), makeMsg("msg-B", "!ping", false))
	s.routeMessage(context.Background(), makeMsg("msg-C", "!ping", false))
	s.routeMessage(context.Background(), makeMsg("", "!ping", false))
	s.routeMessage(context.Background(), makeMsg("", "!ping", false))
	if calls != 4 {
		t.Fatalf("ping ran %d times, want 4 (msg-B once, msg-C once, two ID-less)", calls)
	}
}

func TestRememberMessageWindowIsBounded(t *testing.T) {
	s := NewService(&recordingBackend{}, Config{}, discardLogger())
	for i := 0; i <= seenMessageCacheSize; i++ {
		s.rememberMessage(strings.Repeat("x", i+1))
	}
	if len(s.seenOrder) != seenMessageCacheSize || len(s.seenMessages) != seenMessageCacheSize {
		t.Fatalf("window = %d/%d, want %d", len(s.seenOrder), len(s.seenMessages), seenMessageCacheSize)
	}
	if !s.rememberMessage("x") {
		t.Fatal("oldest ID was not evicted from the window")
	}
}

func TestRouteMessage_EmptyAllowlistRepliesNotAuthorized(t *testing.T) {
	s := NewService(&recordingBackend{}, Config{}, discardLogger())
	s.routeMessage(context.Background(), Message{ID: "1", Text: "!status", AuthorID: "lo`cal"})
	var sent []string
	drainQueue(s, &sent)
	if len(sent) != 1 || !strings.Contains(sent[0], "`local` is not authorized") {
		t.Fatalf("replies = %#v, want a single not-authorized reply", sent)
	}
}

func TestRouteMessage_PlainApproveRecordsSkippedSignal(t *testing.T) {
	store := &testPersonaStore{records: map[string]persona.Record{
		"alice": {Depth: persona.DepthTechnical, SummaryLength: persona.SummaryStandard},
	}}
	s := newLearningService(t, store, true, nil)
	s.pendingCheckpoints[s.pendingCheckpointKey("acme/w#1")] = &pendingCheckpoint{
		RunKey:  "acme/w#1",
		Gen:     2,
		Authors: map[string]struct{}{"alice": {}},
		Payload: runCheckpointPayload{
			RunKey:    "acme/w#1",
			Gen:       2,
			Decisions: []runCheckpointDecision{{Action: "approve", Method: "POST", URL: "/api/plan/acme/approve"}},
		},
	}
	s.routeMessage(context.Background(), Message{ID: "1", Text: "approve", AuthorID: "alice"})
	var sent []string
	drainQueue(s, &sent)
	if len(sent) == 0 || !strings.Contains(sent[len(sent)-1], "Approved") {
		t.Fatalf("replies = %#v", sent)
	}
	if got := store.records["alice"].Learning; got == nil || got.Signals.Skipped != 1 {
		t.Fatalf("plain approve skipped counter = %#v, want 1", got)
	}
}

func TestDiffRunsForgetsPersonaMarksWhenRunLeavesPending(t *testing.T) {
	s := NewService(&recordingBackend{}, Config{}, discardLogger())
	mark := func(author, run string) {
		s.shownSummaries[s.personaRunKey(author, run)] = struct{}{}
		s.expandedRuns[s.personaRunKey(author, run)] = struct{}{}
	}
	mark("alice", "acme/w#1")
	mark("bob", "acme/w#1")
	mark("bob", "acme/w#2")
	mark("bob", "acme/w#3")
	prev := []runSnapshot{
		{Key: "acme/w#1", WaitingOn: "human"},
		{Key: "acme/w#2", WaitingOn: "agent"},
		{Key: "acme/w#3", WaitingOn: "agent"},
	}
	cur := []runSnapshot{
		{Key: "acme/w#1", WaitingOn: "agent"},
		{Key: "acme/w#3", WaitingOn: "agent"},
	}
	s.diffRuns(prev, cur)
	if len(s.shownSummaries) != 1 || len(s.expandedRuns) != 1 {
		t.Fatalf("marks left = %v / %v, want only acme/w#3", s.shownSummaries, s.expandedRuns)
	}
	if _, ok := s.shownSummaries[s.personaRunKey("bob", "acme/w#3")]; !ok {
		t.Fatalf("still-active run mark was dropped: %v", s.shownSummaries)
	}
}
