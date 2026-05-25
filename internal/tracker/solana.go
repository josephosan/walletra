package tracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"walletra/internal/models"
	"walletra/internal/repo"
)

type SolanaProvider struct {
	rpc    string
	client *http.Client
}

func NewSolanaProvider() *SolanaProvider {
	return &SolanaProvider{rpc: "https://api.mainnet-beta.solana.com", client: &http.Client{Timeout: 25 * time.Second}}
}
func (p *SolanaProvider) Name() string                    { return "solana-rpc" }
func (p *SolanaProvider) SupportsChain(chain string) bool { return chain == "solana-mainnet" }
func (p *SolanaProvider) HealthCheck(ctx context.Context) error {
	_, err := p.rpcCall(ctx, "getHealth", []any{})
	return err
}

func (p *SolanaProvider) FetchWalletTransactions(ctx context.Context, wallet models.Wallet, since time.Time, _ []string) ([]models.WalletTransaction, error) {
	sigResp, err := p.rpcCall(ctx, "getSignaturesForAddress", []any{wallet.Address, map[string]any{"limit": 50}})
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Result []struct {
			Signature string `json:"signature"`
			BlockTime *int64 `json:"blockTime"`
		} `json:"result"`
	}
	if err := json.Unmarshal(sigResp, &parsed); err != nil {
		return nil, err
	}
	out := make([]models.WalletTransaction, 0)
	for _, s := range parsed.Result {
		if s.BlockTime == nil {
			continue
		}
		ts := time.Unix(*s.BlockTime, 0).UTC()
		if ts.Before(since) {
			continue
		}
		out = append(out, models.WalletTransaction{WalletID: wallet.ID, TxHash: s.Signature, Chain: wallet.Chain, TokenSymbol: "SOL", Direction: "unknown", Amount: 0, Timestamp: ts, RawPayload: repo.EncodePayload(s)})
	}
	return out, nil
}

func (p *SolanaProvider) rpcCall(ctx context.Context, method string, params []any) ([]byte, error) {
	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.rpc, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	res, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("solana status: %d", res.StatusCode)
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return nil, err
	}
	if e, ok := raw["error"]; ok && len(strings.TrimSpace(string(e))) > 0 && string(e) != "null" {
		return nil, fmt.Errorf("solana rpc error: %s", string(e))
	}
	return raw["result"], nil
}
