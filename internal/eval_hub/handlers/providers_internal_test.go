package handlers_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/eval-hub/eval-hub/internal/eval_hub/constants"
	"github.com/eval-hub/eval-hub/internal/eval_hub/executioncontext"
	"github.com/eval-hub/eval-hub/internal/eval_hub/handlers"
	"github.com/eval-hub/eval-hub/internal/testhelpers"
	"github.com/eval-hub/eval-hub/pkg/api"
)

func TestHandleGetProviderHidesInternalOnlyProvider(t *testing.T) {
	provider := api.ProviderResource{
		Resource: api.Resource{ID: "evalhub-internal"},
		ProviderConfig: api.ProviderConfig{
			Name:         "EvalHub Internal",
			InternalOnly: true,
		},
	}
	storage := &fakeStorage{providerConfigs: map[string]api.ProviderResource{
		provider.Resource.ID: provider,
	}}
	handler := handlers.New(storage, testhelpers.NewValidator(t), &fakeRuntime{}, nil, nil, nil, nil)
	req := &providersRequest{
		MockRequest: createMockRequest("GET", "/api/v1/evaluations/providers/evalhub-internal"),
		pathValues: map[string]string{
			constants.PathParameterProviderID: provider.Resource.ID,
		},
	}
	recorder := httptest.NewRecorder()
	ctx := executioncontext.NewExecutionContext(context.Background(), "req-internal-provider", slog.Default(), "user", "tenant")

	handler.HandleGetProvider(ctx, req, MockResponseWrapper{recorder: recorder})

	if recorder.Code != 404 {
		t.Fatalf("expected 404 for internal provider, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleListProvidersOmitsInternalOnlyProvider(t *testing.T) {
	storage := &listProvidersStorage{
		fakeStorage: &fakeStorage{},
		providers: []api.ProviderResource{
			{
				Resource: api.Resource{ID: "evalhub-internal"},
				ProviderConfig: api.ProviderConfig{
					Name:         "EvalHub Internal",
					InternalOnly: true,
				},
			},
			{
				Resource:       api.Resource{ID: "public-provider"},
				ProviderConfig: api.ProviderConfig{Name: "Public Provider"},
			},
		},
	}
	handler := handlers.New(storage, testhelpers.NewValidator(t), &fakeRuntime{}, nil, nil, nil, nil)
	req := &providersRequest{
		MockRequest: createMockRequest("GET", "/api/v1/evaluations/providers"),
		queryValues: map[string][]string{},
		pathValues:  map[string]string{},
	}
	recorder := httptest.NewRecorder()
	ctx := executioncontext.NewExecutionContext(context.Background(), "req-internal-provider-list", slog.Default(), "user", "tenant")

	handler.HandleListProviders(ctx, req, MockResponseWrapper{recorder: recorder})

	if recorder.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var got api.ProviderResourceList
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatalf("decode provider list: %v", err)
	}
	if got.TotalCount != 1 || len(got.Items) != 1 || got.Items[0].Resource.ID != "public-provider" {
		t.Fatalf("unexpected public provider list: total=%d items=%+v", got.TotalCount, got.Items)
	}
	if value, ok := storage.filter.Params["internal_only"]; !ok || value != false {
		t.Fatalf("provider list did not request public providers: filter=%+v", storage.filter)
	}
}
