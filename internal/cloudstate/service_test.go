package cloudstate

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"fennec/internal/runta"
)

type backendFixture struct {
	owner        string
	runtime      runta.Runtime
	missing      bool
	deleteError  error
	deletes      int
	seenRevision uint64
}

func (b *backendFixture) CurrentUser(context.Context) (string, error) { return b.owner, nil }
func (b *backendFixture) Get(_ context.Context, id string) (runta.Runtime, error) {
	if b.missing {
		return runta.Runtime{}, &runta.APIError{Status: 404}
	}
	return b.runtime, nil
}
func (b *backendFixture) DeleteOwnedShared(_ context.Context, id, owner string, revision uint64) error {
	b.deletes++
	b.seenRevision = revision
	if id != b.runtime.ID || owner != b.runtime.Owner {
		return fmt.Errorf("incorrect target")
	}
	if b.deleteError != nil {
		return b.deleteError
	}
	b.missing = true
	return nil
}
func fixtureService(t *testing.T) (*Service, *backendFixture, *int) {
	t.Helper()
	store := New(t.TempDir())
	quiesced := 0
	service := &Service{Store: store, Build: "fixture-build", Quiesce: func(_ context.Context, owner, runtime string) error {
		if !errors.Is(store.Check(owner), ErrDisabled) {
			t.Error("shutdown occurred before durable intent")
		}
		if runtime != "original" {
			t.Error("shutdown targeted replacement")
		}
		quiesced++
		return nil
	}}
	api := &backendFixture{owner: "owner", runtime: runta.Runtime{ID: "original", Owner: "owner", Name: "Cloud computer", Revision: 7, Environment: map[string]string{"FENNEC_PURPOSE": "shared-v1"}}}
	return service, api, &quiesced
}
func TestDeleteRequiresExactFreshConfirmation(t *testing.T) {
	for _, reason := range []string{"wrong-target", "expired", "account-changed", "build-changed"} {
		t.Run(reason, func(t *testing.T) {
			s, b, q := fixtureService(t)
			plan, err := s.Preview(context.Background(), b, "original")
			if err != nil {
				t.Fatal(err)
			}
			confirmed := "original"
			switch reason {
			case "wrong-target":
				confirmed = "replacement"
			case "expired":
				plan.Expires = time.Now().Add(-time.Second)
				s.plans[plan.ID] = plan
			case "account-changed":
				b.owner = "other"
			case "build-changed":
				s.Build = "replacement-build"
			}
			if _, err = s.Delete(context.Background(), b, plan.ID, confirmed); err == nil {
				t.Fatal("invalid confirmation accepted")
			}
			if b.deletes != 0 || *q != 0 {
				t.Fatal("invalid confirmation mutated runtime")
			}
			if err = s.Store.Check("owner"); err != nil {
				t.Fatal("invalid confirmation changed local intent", err)
			}
		})
	}
}
func TestDeletionFailureSurvivesRestartWithoutRetargeting(t *testing.T) {
	s, b, q := fixtureService(t)
	ctx := context.Background()
	plan, err := s.Preview(ctx, b, "original")
	if err != nil {
		t.Fatal(err)
	}
	b.deleteError = context.DeadlineExceeded
	if _, err = s.Delete(ctx, b, plan.ID, "original"); err == nil {
		t.Fatal("unknown outcome reported success")
	}
	if b.deletes != 1 || *q != 1 || !errors.Is(s.Store.Check("owner"), ErrDisabled) {
		t.Fatal("unknown outcome lost guard")
	}
	if s.CreateAgain(ctx, b, "original", plan.Target.Request) == nil {
		t.Fatal("unknown outcome allowed recreation")
	}
	// Retry after process restart preserves the approved revision even if GET
	// now reports a newer deleting revision. The backend revalidates ownership.
	restarted := &Service{Store: &Store{Directory: s.Store.Directory}, Build: s.Build, Quiesce: s.Quiesce}
	b.runtime.Revision = 8
	b.runtime.Status = "deleting"
	recovery, err := restarted.Preview(ctx, b, "original")
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Target.Revision != 7 || recovery.Target.Request != plan.Target.Request {
		t.Fatal("recovery retargeted deletion", recovery)
	}
	b.deleteError = nil
	result, err := restarted.Delete(ctx, b, recovery.ID, "original")
	if err != nil || !result.Deleted {
		t.Fatal(result, err)
	}
	if b.seenRevision != 7 || !errors.Is(restarted.Store.Check("owner"), ErrDisabled) {
		t.Fatal("deleted runtime was automatically recreated")
	}
	if err = restarted.CreateAgain(ctx, b, "original", result.Request); err != nil {
		t.Fatal(err)
	}
	if err = restarted.Store.Check("owner"); err != nil {
		t.Fatal(err)
	}
}
func TestQuiesceFailureNeverSendsDelete(t *testing.T) {
	s, b, _ := fixtureService(t)
	s.Quiesce = func(context.Context, string, string) error { return context.DeadlineExceeded }
	p, err := s.Preview(context.Background(), b, "original")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Delete(context.Background(), b, p.ID, "original"); err == nil {
		t.Fatal("failed shutdown accepted")
	}
	if b.deletes != 0 {
		t.Fatal("delete issued while services still active")
	}
	if !errors.Is(s.Store.Check("owner"), ErrDisabled) {
		t.Fatal("shutdown failure dropped durable guard")
	}
}

func TestCreateAgainInvalidatesAllOldDeletionPreviews(t *testing.T) {
	s, b, _ := fixtureService(t)
	ctx := context.Background()
	first, err := s.Preview(ctx, b, "original")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := s.Preview(ctx, b, "original")
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := s.Delete(ctx, b, first.ID, "original")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateAgain(ctx, b, "original", deleted.Request); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Delete(ctx, b, stale.ID, "original"); err == nil {
		t.Fatal("old confirmation survived explicit creation")
	}
	if b.deletes != 1 {
		t.Fatal("old preview replayed deletion")
	}
	if err = s.Store.Check("owner"); err != nil {
		t.Fatal("stale preview blocked replacement creation", err)
	}
}

func TestCloudChangesAfterPreviewDoNotDisruptLocalServices(t *testing.T) {
	for _, change := range []string{"revision", "owner", "purpose"} {
		t.Run(change, func(t *testing.T) {
			s, b, q := fixtureService(t)
			ctx := context.Background()
			plan, err := s.Preview(ctx, b, "original")
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "revision":
				b.runtime.Revision++
			case "owner":
				b.runtime.Owner = "other"
			case "purpose":
				b.runtime.Environment["FENNEC_PURPOSE"] = "unrelated"
			}
			if _, err = s.Delete(ctx, b, plan.ID, "original"); err == nil {
				t.Fatal("changed runtime accepted")
			}
			if *q != 0 || b.deletes != 0 {
				t.Fatal("stale preview disrupted local work")
			}
			if err = s.Store.Check("owner"); err != nil {
				t.Fatal("stale preview left automatic preparation blocked", err)
			}
		})
	}
}

func TestRejectedDeleteRequiresNewPreviewButUnknownOutcomeDoesNotRebase(t *testing.T) {
	for _, rejected := range []bool{true, false} {
		t.Run(fmt.Sprint(rejected), func(t *testing.T) {
			s, b, _ := fixtureService(t)
			ctx := context.Background()
			p, err := s.Preview(ctx, b, "original")
			if err != nil {
				t.Fatal(err)
			}
			b.deleteError = context.DeadlineExceeded
			if rejected {
				b.deleteError = runta.ErrDeletionRejected
			}
			if _, err = s.Delete(ctx, b, p.ID, "original"); err == nil {
				t.Fatal("failure reported success")
			}
			record, err := s.Store.Read("owner")
			if err != nil || record.Rejected != rejected {
				t.Fatal(record, err)
			}
			if err = s.Store.Check("owner"); !errors.Is(err, ErrDisabled) {
				t.Fatal("failure allowed automatic creation")
			}
			b.runtime.Revision = 8
			fresh, err := s.Preview(ctx, b, "original")
			if err != nil {
				t.Fatal(err)
			}
			if rejected {
				if fresh.Target.Revision != 8 || fresh.Target.Request == p.Target.Request {
					t.Fatal("explicit re-review did not bind new revision")
				}
				b.deleteError = nil
				if _, err = s.Delete(ctx, b, fresh.ID, "original"); err != nil {
					t.Fatal(err)
				}
				if b.seenRevision != 8 {
					t.Fatal("new confirmation used old revision")
				}
			} else {
				if fresh.Target.Revision != 7 || fresh.Target.Request != p.Target.Request {
					t.Fatal("unknown outcome rebased deletion")
				}
				if _, err = s.Delete(ctx, b, fresh.ID, "original"); err == nil {
					t.Fatal("unknown outcome retried changed runtime")
				}
				if b.deletes != 1 {
					t.Fatal("unknown DELETE replayed")
				}
			}
		})
	}
}
