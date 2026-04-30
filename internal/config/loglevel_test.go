package config

import (
	"os"
	"testing"
)

func TestDefaultLogLevelForEnv(t *testing.T) {
	cases := []struct {
		env      AppEnv
		expected string
	}{
		{AppEnvLocal, "debug"},
		{AppEnvStage, "debug"},
		{AppEnvOnprem, "info"},
		{AppEnvProd, "info"},
	}
	for _, c := range cases {
		if got := defaultLogLevelForEnv(c.env); got != c.expected {
			t.Errorf("APP_ENV=%v: got %q, want %q", c.env, got, c.expected)
		}
	}
}

func TestParseLoggingConfigDefaultsToAppEnv(t *testing.T) {
	os.Unsetenv("LOG_LEVEL")
	cases := map[AppEnv]string{
		AppEnvLocal:  "debug",
		AppEnvStage:  "debug",
		AppEnvOnprem: "info",
		AppEnvProd:   "info",
	}
	for env, want := range cases {
		got := parseLoggingConfig(env).Level
		if got != want {
			t.Errorf("APP_ENV=%v: got %q, want %q", env, got, want)
		}
	}
}

func TestParseLoggingConfigIgnoresLogLevelEnv(t *testing.T) {
	// LOG_LEVEL env 가 설정돼도 무시되고 APP_ENV 가 결정.
	os.Setenv("LOG_LEVEL", "warn")
	defer os.Unsetenv("LOG_LEVEL")
	got := parseLoggingConfig(AppEnvProd).Level
	if got != "info" {
		t.Errorf("LOG_LEVEL env should be ignored; APP_ENV=prod expects info, got %q", got)
	}
}
