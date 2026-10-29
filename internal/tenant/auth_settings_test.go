package tenant

import "testing"

func TestAuthSettings_Defaults(t *testing.T) {
	var a AuthSettings

	if got := a.AccessTTLSeconds(); got != defaultAccessTTLSeconds {
		t.Errorf("AccessTTLSeconds default = %d, want %d", got, defaultAccessTTLSeconds)
	}
	if got := a.RefreshTTLSeconds(); got != defaultRefreshTTLSeconds {
		t.Errorf("RefreshTTLSeconds default = %d, want %d", got, defaultRefreshTTLSeconds)
	}
}

func TestAuthSettings_Overrides(t *testing.T) {
	access := 900
	refresh := 5184000
	a := AuthSettings{AccessTokenTTL: &access, RefreshTokenTTL: &refresh}

	if got := a.AccessTTLSeconds(); got != access {
		t.Errorf("AccessTTLSeconds = %d, want %d", got, access)
	}
	if got := a.RefreshTTLSeconds(); got != refresh {
		t.Errorf("RefreshTTLSeconds = %d, want %d", got, refresh)
	}
	if got := a.AccessTTL().Seconds(); got != float64(access) {
		t.Errorf("AccessTTL = %v, want %ds", got, access)
	}
}

func TestAuthSettings_NonPositiveFallsBackToDefault(t *testing.T) {
	zero := 0
	neg := -5
	a := AuthSettings{AccessTokenTTL: &zero, RefreshTokenTTL: &neg}

	if got := a.AccessTTLSeconds(); got != defaultAccessTTLSeconds {
		t.Errorf("AccessTTLSeconds(0) = %d, want default %d", got, defaultAccessTTLSeconds)
	}
	if got := a.RefreshTTLSeconds(); got != defaultRefreshTTLSeconds {
		t.Errorf("RefreshTTLSeconds(-5) = %d, want default %d", got, defaultRefreshTTLSeconds)
	}
}
