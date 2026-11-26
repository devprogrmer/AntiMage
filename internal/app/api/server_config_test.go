package api

import "testing"

func TestLoadConfigNetworkIntelligenceOptInPreservesProxyConfiguration(t *testing.T) {
	t.Setenv("SQLALCHEMY_DATABASE_URL", "sqlite:///config-test.db")
	t.Setenv("ANTIMAGE_TRUSTED_PROXY_CIDRS", "127.0.0.1/32, ::1/128")
	for _, enabled := range []string{"", "true", "false", "invalid"} {
		t.Setenv("ANTIMAGE_NETWORK_INTELLIGENCE_ENABLED", enabled)
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.NetworkIntelligenceEnabled != (enabled == "true") {
			t.Fatalf("flag %q produced %t", enabled, cfg.NetworkIntelligenceEnabled)
		}
		if len(cfg.TrustedProxyCIDRs) != 2 || cfg.TrustedProxyCIDRs[0] != "127.0.0.1/32" || cfg.TrustedProxyCIDRs[1] != "::1/128" {
			t.Fatalf("proxy configuration=%v", cfg.TrustedProxyCIDRs)
		}
	}
}

func TestLoadConfigPrefersSQLAlchemyDatabaseURL(t *testing.T) {
	t.Setenv("SQLALCHEMY_DATABASE_URL", "sqlite:///legacy.db")
	t.Setenv("DATABASE_URL", "sqlite:///new.db")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database != "sqlite:///legacy.db" {
		t.Fatalf("Database=%q want %q", cfg.Database, "sqlite:///legacy.db")
	}
}

func TestLoadConfigFallsBackToDatabaseURL(t *testing.T) {
	t.Setenv("SQLALCHEMY_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "sqlite:///fallback.db")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database != "sqlite:///fallback.db" {
		t.Fatalf("Database=%q want %q", cfg.Database, "sqlite:///fallback.db")
	}
}

func TestLoadConfigUsesRecordingDefaultsFromDatabaseSettings(t *testing.T) {
	t.Setenv("SQLALCHEMY_DATABASE_URL", "sqlite:///usage-flags.db")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RecordNodeUsage {
		t.Fatal("RecordNodeUsage=false want true")
	}
	if !cfg.RecordNodeUserUsages {
		t.Fatal("RecordNodeUserUsages=false want true")
	}
}
