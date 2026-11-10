package config

import "testing"

func TestParseConfigBytesClients(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`clients:
  - id: alice
    api-key: client-secret
    quotas:
      codex: {weekly-usd: 50}
      deepseek: {limit-cny: 100}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Clients) != 1 || cfg.Clients[0].ID != "alice" || cfg.Clients[0].Quotas.Codex.WeeklyUSD != 50 || cfg.Clients[0].Quotas.DeepSeek.LimitCNY != 100 {
		t.Fatalf("unexpected clients config: %+v", cfg.Clients)
	}
}

func TestParseConfigBytesRejectsDuplicateClientIdentity(t *testing.T) {
	_, err := ParseConfigBytes([]byte(`clients:
  - {id: alice, api-key: first, quotas: {codex: {weekly-usd: 1}}}
  - {id: alice, api-key: second, quotas: {deepseek: {limit-cny: 1}}}
`))
	if err == nil {
		t.Fatal("duplicate client ID was accepted")
	}
}
