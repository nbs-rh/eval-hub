package sql_test

import (
	"testing"
	"time"

	"github.com/eval-hub/eval-hub/internal/eval_hub/abstractions"
	"github.com/eval-hub/eval-hub/pkg/api"
)

func TestProviderStorageHidesInternalProvidersBeforePagination(t *testing.T) {
	store, err := getTestStorage(t, "sqlite", getDBName())
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	providers := map[string]api.ProviderResource{
		"public-provider": {
			Resource:       api.Resource{ID: "public-provider", Owner: "system"},
			ProviderConfig: api.ProviderConfig{Name: "Public Provider"},
		},
		"evalhub-internal": {
			Resource: api.Resource{ID: "evalhub-internal", Owner: "system"},
			ProviderConfig: api.ProviderConfig{
				Name:         "EvalHub Internal",
				InternalOnly: true,
			},
		},
	}
	if err := store.LoadSystemResources(nil, providers); err != nil {
		t.Fatalf("load system providers: %v", err)
	}

	internal, err := store.GetProvider("evalhub-internal")
	if err != nil {
		t.Fatalf("get internal provider for runtime use: %v", err)
	}
	if !internal.InternalOnly {
		t.Fatal("internal_only marker was not persisted")
	}

	page, err := store.GetProviders(&abstractions.QueryFilter{
		Limit:  1,
		Offset: 0,
		Params: map[string]any{
			"scope":         abstractions.ScopeSystem,
			"internal_only": false,
		},
	})
	if err != nil {
		t.Fatalf("list public providers: %v", err)
	}
	if page.TotalCount != 1 || len(page.Items) != 1 || page.Items[0].Resource.ID != "public-provider" {
		t.Fatalf("expected only public provider on first page, got total=%d items=%+v", page.TotalCount, page.Items)
	}

	nextPage, err := store.GetProviders(&abstractions.QueryFilter{
		Limit:  1,
		Offset: 1,
		Params: map[string]any{
			"scope":         abstractions.ScopeSystem,
			"internal_only": false,
		},
	})
	if err != nil {
		t.Fatalf("list public providers page 2: %v", err)
	}
	if nextPage.TotalCount != 1 || len(nextPage.Items) != 0 {
		t.Fatalf("unexpected second public-provider page: total=%d items=%+v", nextPage.TotalCount, nextPage.Items)
	}
}

func TestProviderStorage(t *testing.T) {
	tenant := api.Tenant("tenant-1")
	store, err := getTestStorage(t, "sqlite", getDBName())
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	store = store.WithTenant(tenant)

	provider := &api.ProviderResource{
		Resource: api.Resource{
			ID:        "provider-1",
			CreatedAt: time.Now(),
			Tenant:    tenant,
		},
		ProviderConfig: api.ProviderConfig{
			Name:        "Test Provider",
			Description: "A test provider",
			Benchmarks: []api.BenchmarkResource{
				{
					ID:          "bench-1",
					Name:        "Benchmark 1",
					Description: "First benchmark",
				},
			},
		},
	}

	t.Run("CreateUserProvider creates a new provider", func(t *testing.T) {
		err := store.CreateProvider(provider)
		if err != nil {
			t.Fatalf("CreateUserProvider failed: %v", err)
		}
	})

	t.Run("GetUserProvider returns the provider", func(t *testing.T) {
		got, err := store.GetProvider("provider-1")
		if err != nil {
			t.Fatalf("GetUserProvider failed: %v", err)
		}
		if got.Resource.ID != "provider-1" {
			t.Errorf("Expected ID provider-1, got %s", got.Resource.ID)
		}
		if got.Name != "Test Provider" {
			t.Errorf("Expected Name Test Provider, got %s", got.Name)
		}
		if len(got.Benchmarks) != 1 {
			t.Errorf("Expected 1 benchmark, got %d", len(got.Benchmarks))
		}
		if got.Benchmarks[0].ID != "bench-1" {
			t.Errorf("Expected benchmark ID bench-1, got %s", got.Benchmarks[0].ID)
		}
	})

	t.Run("UpdateProvider updates the provider config", func(t *testing.T) {
		updated := &api.ProviderConfig{
			Name:        "Updated Provider",
			Description: "Updated description",
			Benchmarks: []api.BenchmarkResource{
				{ID: "bench-1", Name: "Bench 1"},
				{ID: "bench-2", Name: "Bench 2"},
			},
		}
		got, err := store.UpdateProvider("provider-1", updated)
		if err != nil {
			t.Fatalf("UpdateProvider failed: %v", err)
		}
		if got.Name != "Updated Provider" {
			t.Errorf("Expected Name Updated Provider, got %s", got.Name)
		}
		if got.Description != "Updated description" {
			t.Errorf("Expected Description Updated description, got %s", got.Description)
		}
		if len(got.Benchmarks) != 2 {
			t.Errorf("Expected 2 benchmarks, got %d", len(got.Benchmarks))
		}
	})

	t.Run("PatchProvider patches the provider config", func(t *testing.T) {
		patches := api.Patch{
			{Op: api.PatchOpReplace, Path: "/description", Value: "Patched description"},
		}
		got, err := store.PatchProvider("provider-1", &patches)
		if err != nil {
			t.Fatalf("PatchProvider failed: %v", err)
		}
		if got.Description != "Patched description" {
			t.Errorf("Expected Description Patched description, got %s", got.Description)
		}
		if got.Name != "Updated Provider" {
			t.Errorf("Expected Name unchanged, got %s", got.Name)
		}
	})

	t.Run("GetProviders with name filter returns matching providers", func(t *testing.T) {
		filter := &abstractions.QueryFilter{
			Limit:  10,
			Offset: 0,
			Params: map[string]any{"name": "Updated Provider"},
		}
		got, err := store.GetProviders(filter)
		if err != nil {
			t.Fatalf("GetProviders failed: %v", err)
		}
		if got.TotalCount != 1 {
			t.Errorf("Expected 1 provider, got total_count=%d", got.TotalCount)
		}
		if len(got.Items) != 1 {
			t.Errorf("Expected 1 item, got %d", len(got.Items))
		}
		if len(got.Items) > 0 && got.Items[0].Name != "Updated Provider" {
			t.Errorf("Expected name Updated Provider, got %s", got.Items[0].Name)
		}
	})

	t.Run("GetProviders with tags filter returns matching providers", func(t *testing.T) {
		providerWithTags := &api.ProviderResource{
			Resource: api.Resource{
				ID:        "provider-2",
				CreatedAt: time.Now(),
				Tenant:    api.Tenant("tenant-1"),
			},
			ProviderConfig: api.ProviderConfig{
				Name:        "Tagged Provider",
				Description: "Provider with tags",
				Tags:        []string{"list-test-tag", "searchable"},
			},
		}
		if err := store.CreateProvider(providerWithTags); err != nil {
			t.Fatalf("CreateProvider failed: %v", err)
		}
		t.Cleanup(func() {
			if err := store.DeleteProvider("provider-2"); err != nil {
				t.Errorf("DeleteProvider(provider-2) cleanup: %v", err)
			}
		})

		filter := &abstractions.QueryFilter{
			Limit:  10,
			Offset: 0,
			Params: map[string]any{"tags": "list-test-tag"},
		}
		got, err := store.GetProviders(filter)
		if err != nil {
			t.Fatalf("GetProviders failed: %v", err)
		}
		if got.TotalCount != 1 {
			t.Errorf("Expected 1 provider with tag, got total_count=%d", got.TotalCount)
		}
		if len(got.Items) > 0 && got.Items[0].Name != "Tagged Provider" {
			t.Errorf("Expected name Tagged Provider, got %s", got.Items[0].Name)
		}
	})

	t.Run("GetUserProvider returns not found for missing provider", func(t *testing.T) {
		_, err := store.GetProvider("non-existent")
		if err == nil {
			t.Fatal("Expected error for non-existent provider")
		}
	})

	t.Run("DeleteUserProvider removes the provider", func(t *testing.T) {
		err := store.DeleteProvider("provider-1")
		if err != nil {
			t.Fatalf("DeleteUserProvider failed: %v", err)
		}

		_, err = store.GetProvider("provider-1")
		if err == nil {
			t.Fatal("Expected error after delete, provider should not exist")
		}
	})
}
