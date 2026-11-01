package contextbuild_test

import (
	"context"
	"github.com/kzs0/reviewd/internal/contextbuild"
	"github.com/kzs0/reviewd/internal/model"
	"github.com/kzs0/reviewd/internal/storage"
	"testing"
)

func TestSummaryPublicationRequiresExactPinnedBase(t *testing.T) {
	ctx := context.Background()
	s, e := storage.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	o := summaryOptions()
	if e = contextbuild.Publish(ctx, s, o.Snapshot); e != nil {
		t.Fatal(e)
	}
	fake := &model.Fake{Response: model.Response{Raw: []byte(`{"schema_version":1,"summary":"notes","evidence_ids":["a.go"]}`)}}
	build, e := contextbuild.Summarize(ctx, s, fake, o)
	if e != nil {
		t.Fatal(e)
	}
	modified := o.Snapshot
	modified.Artifacts = append([]contextbuild.Artifact(nil), modified.Artifacts...)
	modified.Artifacts[0].Summary = "invented base behavior"
	if _, e = contextbuild.PublishSummaries(ctx, s, modified, build); e == nil {
		t.Fatal("mutated base sharing pinned ID was published")
	}
	if _, e = contextbuild.PublishSummaries(ctx, s, o.Snapshot, build); e != nil {
		t.Fatal("canonical pinned base rejected", e)
	}
}
