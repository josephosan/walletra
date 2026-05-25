package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"walletra/internal/models"
	"walletra/internal/repo"
)

type TONProvider struct{ client *http.Client }

func NewTONProvider() *TONProvider {
	return &TONProvider{client: &http.Client{Timeout: 20 * time.Second}}
}
func (p *TONProvider) Name() string                    { return "toncenter" }
func (p *TONProvider) SupportsChain(chain string) bool { return chain == "ton-mainnet" }
func (p *TONProvider) HealthCheck(ctx context.Context) error {
	u := "https://toncenter.com/api/v2/getMasterchainInfo"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	res, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("toncenter status: %d", res.StatusCode)
	}
	return nil
}

type tonResp struct {
	Ok     bool `json:"ok"`
	Result []struct {
		TransactionID struct {
			LT   string `json:"lt"`
			Hash string `json:"hash"`
		} `json:"transaction_id"`
		Utime int64 `json:"utime"`
		InMsg struct {
			Value string `json:"value"`
		} `json:"in_msg"`
	} `json:"result"`
}

func (p *TONProvider) FetchWalletTransactions(ctx context.Context, wallet models.Wallet, since time.Time, _ []string) ([]models.WalletTransaction, error) {
	u, _ := url.Parse("https://toncenter.com/api/v2/getTransactions")
	q := u.Query()
	q.Set("address", wallet.Address)
	q.Set("limit", "50")
	q.Set("to_lt", "0")
	q.Set("archival", "true")
	u.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	res, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("ton tx status: %d", res.StatusCode)
	}
	var payload tonResp
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, err
	}
	out := make([]models.WalletTransaction, 0, len(payload.Result))
	for _, it := range payload.Result {
		ts := time.Unix(it.Utime, 0).UTC()
		if ts.Before(since) {
			continue
		}
		valNano, _ := strconv.ParseFloat(strings.TrimSpace(it.InMsg.Value), 64)
		amt := valNano / 1e9
		hash := it.TransactionID.Hash
		if hash == "" {
			hash = it.TransactionID.LT
		}
		out = append(out, models.WalletTransaction{WalletID: wallet.ID, TxHash: hash, Chain: wallet.Chain, TokenSymbol: "TON", Direction: "unknown", Amount: amt, Timestamp: ts, RawPayload: repo.EncodePayload(it)})
	}
	return out, nil
}
