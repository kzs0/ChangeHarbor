package model_test

import (
	"context"
	"github.com/kzs0/reviewd/internal/model"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCostAndTokenReservationsCannotOverflow(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer s.Close()
	a := adapter(s.URL)
	a.Capabilities.PricingKnown = true
	a.Capabilities.InputPriceMicrosPerMillion = math.MaxInt64
	a.Capabilities.OutputPriceMicrosPerMillion = math.MaxInt64
	a.Limits.MaxCostMicros = 100
	if _, e := a.Complete(context.Background(), model.Request{Prompt: []byte("source")}); e == nil {
		t.Fatal("overflow price accepted")
	}
	a = adapter(s.URL)
	a.Capabilities.MaxOutputTokens = math.MaxInt
	a.Capabilities.ContextTokens = math.MaxInt
	a.Limits.MaxOutputTokens = math.MaxInt
	a.Limits.MaxTotalTokens = math.MaxInt64
	if _, e := a.Complete(context.Background(), model.Request{Prompt: []byte("source")}); e == nil {
		t.Fatal("overflow token reservation accepted")
	}
	if calls != 0 {
		t.Fatal("invalid budget made call")
	}
}
