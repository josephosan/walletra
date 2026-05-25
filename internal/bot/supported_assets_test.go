package bot

import "testing"

func TestSupportedChains_ExpectedSet(t *testing.T) {
	expected := []string{
		"matic-mainnet",
	}

	assertNoDuplicates(t, supportedChains, "supportedChains")
	for _, chain := range expected {
		if !isSupported(supportedChains, chain) {
			t.Fatalf("missing supported chain: %s", chain)
		}
	}
	if len(supportedChains) != len(expected) {
		t.Fatalf("supportedChains size changed: got=%d expected=%d", len(supportedChains), len(expected))
	}
}

func TestSupportedBaseCoins_ExpectedSet(t *testing.T) {
	expected := []string{
		"MATIC",
	}

	assertNoDuplicates(t, supportedBaseCoins, "supportedBaseCoins")
	for _, coin := range expected {
		if !isSupported(supportedBaseCoins, coin) {
			t.Fatalf("missing supported base coin: %s", coin)
		}
	}
	if len(supportedBaseCoins) != len(expected) {
		t.Fatalf("supportedBaseCoins size changed: got=%d expected=%d", len(supportedBaseCoins), len(expected))
	}
}

func TestChainCoinCoveragePairs(t *testing.T) {
	pairs := map[string]string{
		"matic-mainnet": "MATIC",
	}

	for chain, coin := range pairs {
		if !isSupported(supportedChains, chain) {
			t.Fatalf("pair references unsupported chain: %s", chain)
		}
		if !isSupported(supportedBaseCoins, coin) {
			t.Fatalf("pair references unsupported coin: %s", coin)
		}
	}
}

func TestSplitCSV_NormalizesTokenSymbols(t *testing.T) {
	got := splitCSV(" pepe, Usdt, link ,, btc ")
	want := []string{"PEPE", "USDT", "LINK", "BTC"}
	if len(got) != len(want) {
		t.Fatalf("unexpected token count: got=%d want=%d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected token at %d: got=%s want=%s", i, got[i], want[i])
		}
	}
}

func assertNoDuplicates(t *testing.T, values []string, listName string) {
	t.Helper()
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			t.Fatalf("duplicate value in %s: %s", listName, v)
		}
		seen[v] = struct{}{}
	}
}
