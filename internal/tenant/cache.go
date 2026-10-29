package tenant

import (
	"context"
	"sync"
	"time"
)

type cacheEntry struct {
	config    *TenantConfig
	expiresAt time.Time
}

// CachedStore wraps any Store with a 30-second in-memory cache.
type CachedStore struct {
	store Store
	cache sync.Map
}

func NewCachedStore(store Store) *CachedStore {
	return &CachedStore{store: store}
}

func (s *CachedStore) GetTenantConfig(ctx context.Context, tenantID string) (*TenantConfig, error) {
	if val, ok := s.cache.Load(tenantID); ok {
		entry := val.(*cacheEntry)
		if time.Now().Before(entry.expiresAt) {
			return entry.config, nil
		}
	}

	cfg, err := s.store.GetTenantConfig(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	s.cache.Store(tenantID, &cacheEntry{
		config:    cfg,
		expiresAt: time.Now().Add(30 * time.Second),
	})
	return cfg, nil
}

// ScanTenantIDs delegates to the underlying store (no caching of the list).
func (s *CachedStore) ScanTenantIDs(ctx context.Context) ([]string, error) {
	return s.store.ScanTenantIDs(ctx)
}

// Invalidate drops every cached config so the next request rereads disk.
func (s *CachedStore) Invalidate() {
	s.cache.Range(func(k, _ any) bool {
		s.cache.Delete(k)
		return true
	})
}
