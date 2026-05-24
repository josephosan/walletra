package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"wallet_tracker_bot/internal/models"
	"wallet_tracker_bot/internal/repo"
)

type TransactionProvider interface {
	FetchWalletTransactions(ctx context.Context, wallet models.Wallet, since time.Time, tokenFilters []string) ([]models.WalletTransaction, error)
}

type CovalentProvider struct {
	apiKey string
	client *http.Client
}

func NewCovalentProvider(apiKey string) *CovalentProvider {
	return &CovalentProvider{
		apiKey: apiKey,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

type covalentResponse struct {
	Data struct {
		Items []struct {
			TxHash    string `json:"tx_hash"`
			BlockTime string `json:"block_signed_at"`
			LogEvents []struct {
				SenderAddress string `json:"sender_address"`
				Decoded       struct {
					Name string `json:"name"`
				} `json:"decoded"`
				RawLogTopics               []string `json:"raw_log_topics"`
				SenderContractTickerSymbol string   `json:"sender_contract_ticker_symbol"`
				SenderContractAddress      string   `json:"sender_address"`
				Params                     []struct {
					Name  string      `json:"name"`
					Value interface{} `json:"value"`
				} `json:"params"`
			} `json:"log_events"`
		} `json:"items"`
	} `json:"data"`
}

func (p *CovalentProvider) FetchWalletTransactions(ctx context.Context, wallet models.Wallet, since time.Time, tokenFilters []string) ([]models.WalletTransaction, error) {
	if p.apiKey == "" {
		return nil, nil
	}
	chain := wallet.Chain
	if chain == "" {
		chain = "eth-mainnet"
	}

	u := fmt.Sprintf("https://api.covalenthq.com/v1/%s/address/%s/transactions_v3/", url.PathEscape(chain), url.PathEscape(wallet.Address))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(p.apiKey, "")
	q := req.URL.Query()
	q.Set("no-logs", "false")
	req.URL.RawQuery = q.Encode()

	res, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("provider status: %d", res.StatusCode)
	}

	var payload covalentResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, err
	}

	filterMap := map[string]bool{}
	for _, f := range tokenFilters {
		filterMap[strings.ToUpper(strings.TrimSpace(f))] = true
	}

	out := make([]models.WalletTransaction, 0)
	for _, it := range payload.Data.Items {
		ts, err := time.Parse(time.RFC3339, it.BlockTime)
		if err != nil || ts.Before(since) {
			continue
		}
		for _, ev := range it.LogEvents {
			token := strings.ToUpper(strings.TrimSpace(ev.SenderContractTickerSymbol))
			if len(filterMap) > 0 && !filterMap[token] {
				continue
			}
			direction := "unknown"
			if strings.Contains(strings.ToLower(ev.Decoded.Name), "transfer") {
				direction = "transfer_out"
			}
			amount := 0.0
			out = append(out, models.WalletTransaction{
				WalletID:    wallet.ID,
				TxHash:      it.TxHash,
				Chain:       chain,
				TokenSymbol: token,
				Direction:   direction,
				Amount:      amount,
				Timestamp:   ts,
				RawPayload:  repo.EncodePayload(ev),
			})
		}
	}
	return out, nil
}
