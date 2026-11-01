package model_test

import (
	"context"
	"github.com/kzs0/reviewd/internal/model"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMissingUsageIsUnknownAndCostNull(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{}"}}]}`))
	}))
	defer s.Close()
	a := adapter(s.URL)
	a.Capabilities.PricingKnown = true
	a.Capabilities.InputPriceMicrosPerMillion = 100
	r, e := a.Complete(context.Background(), model.Request{Prompt: []byte("x")})
	if e != nil || r.Usage.Known || r.Usage.CostMicros != nil {
		t.Fatalf("usage fabricated %+v %v", r.Usage, e)
	}
}
