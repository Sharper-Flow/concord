package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCurrentRemoteIdentities(t *testing.T) {
	for _, tc := range []struct{ raw, current string }{
		{"https://github.com/o/r", "https://github.com/o/r"},
		{"http://host:8080/o/r", "http://host:8080/o/r"},
		{"ssh://git@host/o/r", "ssh://git@host/o/r"},
		{"ssh://git@host:2222/o/r.git", "ssh://git@host:2222/o/r"},
		{"git://host/o/r", "git://host/o/r"},
		{"git@github.com:o/r.git", "ssh://git@github.com/o/r"},
		{"host:o/r@v2.git", "ssh://host/o/r@v2"},
		{"git:pw@host:a://b", "ssh://git/pw@host:a://b"},
		{"file:///srv/o/r.git", ""},
		{"git+ssh://git@host/o/r", ""},
		{"ftp://host/o/r", ""},
		{"https://host", ""},
		{"http://host/", ""},
		{"git://host", ""},
		{"ssh://host", ""},
		{"https:///o/r", ""},
		{"http:///o/r", ""},
		{"git:///o/r", ""},
		{"ssh:///o/r", ""},
		{"ssh://:2222/o/r", ""},
		{" HTTPS://GitHub.Com/O/R.git ", "https://github.com/o/r"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := normalizeRemote(tc.raw)
			if err != nil || got != tc.current {
				t.Errorf("current=%q err=%v want=%q", got, err, tc.current)
			}
		})
	}
}

func TestRemoteNoIdentityOriginUsesOnlyRegisteredPath(t *testing.T) {
	for _, raw := range []string{"file:///srv/o/r.git", "git+ssh://git@host/o/r", "ftp://host/o/r", "https://host", "http://host/", "git://host", "ssh://host", "https:///o/r", "http:///o/r", "git:///o/r", "ssh:///o/r", "ssh://:2222/o/r"} {
		t.Run(raw, func(t *testing.T) {
			ctx := context.Background()
			s := locatorReplayStore(t)
			root := t.TempDir()
			host, err := ResolveProjectHost(ctx, root, root, &locatorGitStub{root: root, remote: raw})
			if err != nil || host.Remote != "" || host.Host.GitRemote != "" {
				t.Fatalf("host=%+v err=%v", host, err)
			}
			_, err = s.MatchResolvedProjectHost(ctx, host)
			assertFailureKind(t, err, KindUnknownScope)
			if err := s.AddProjectLocator(ctx, "project-a", ProjectLocator{ID: "path", Kind: LocatorCanonicalPath, Value: root}, 1); err != nil {
				t.Fatal(err)
			}
			resolved, err := s.MatchResolvedProjectHost(ctx, host)
			if err != nil || resolved.ProjectID != "project-a" || len(resolved.Locators) != 1 || resolved.Locators[0].Kind != LocatorCanonicalPath {
				t.Fatalf("resolved=%+v err=%v", resolved, err)
			}
			before := histEventRows(t, s)
			for _, mutate := range []func(context.Context, string, ProjectLocator, int64) error{s.AddProjectLocator, s.UpdateProjectLocator} {
				err := mutate(ctx, "project-a", ProjectLocator{ID: "path", Kind: LocatorGitRemote, Value: raw}, 2)
				assertFailureKind(t, err, KindInvalidFilter)
				var failure *Failure
				if !errors.As(err, &failure) || !strings.Contains(failure.RecoveryAction, "canonical_path") {
					t.Fatalf("missing path recovery: %v", err)
				}
			}
			if !reflect.DeepEqual(before, histEventRows(t, s)) {
				t.Fatal("unsupported locator mutation wrote an event")
			}
		})
	}
}

func TestRemoteNoIdentityKeepsMalformedRefusals(t *testing.T) {
	for _, raw := range []string{"https:///o/../r", "https://../", "ftp://host/o/../r", "https:///o\\r", "https:///o/\nr", "https://"} {
		t.Run(raw, func(t *testing.T) {
			for _, version := range []int{1, 2} {
				_, err := normalizeRemoteVersion(raw, version)
				assertFailureKind(t, err, KindInvalidFilter)
			}
			root := t.TempDir()
			_, err := ResolveProjectHost(context.Background(), root, root, &locatorGitStub{root: root, remote: raw})
			assertFailureKind(t, err, KindInvalidFilter)
		})
	}
}

func TestLiveRemoteAppendCannotBypassAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, raw, normalized string
		version               int
		kind                  FailureKind
	}{
		{"old writer", "https://host/o/r", "ssh://https/host/o/r", 1, KindInvalidPayload},
		{"credentials", "https://TOKEN@host/o/r", "https://host/o/r", 2, KindInvalidFilter},
		{"unsupported", "file:///srv/o/r", "ssh://file/srv/o/r", 2, KindInvalidPayload},
		{"pathless", "https://host", "ssh://https/host", 2, KindInvalidPayload},
		{"hostless", "https:///o/r", "ssh://https/o/r", 2, KindInvalidPayload},
		{"tampered", "https://host/o/r", "https://host/other/r", 2, KindInvalidPayload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := locatorReplayStore(t)
			before := histEventRows(t, s)
			e := recordedLocatorEvent("attempt", string(LocatorGitRemote), tc.raw, tc.normalized)
			e.PayloadVersion = tc.version
			err := ApplyOperation(context.Background(), s, Operation{Events: []Event{e}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "project-a"): 1}})
			assertFailureKind(t, err, tc.kind)
			assertTableCount(t, s, "project_locators", 0)
			if !reflect.DeepEqual(before, histEventRows(t, s)) {
				t.Fatal("refused generic append changed journal")
			}
		})
	}
}
