package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"walletra/internal/models"
	"walletra/internal/repo"
)

type EVMExplorerProvider struct {
	apiKey string
	client *http.Client
}

func NewEVMExplorerProvider(apiKey string) *EVMExplorerProvider {
	return &EVMExplorerProvider{apiKey: strings.TrimSpace(apiKey), client: &http.Client{Timeout: 20 * time.Second}}
}

func (p *EVMExplorerProvider) Name() string { return "evm-explorer" }
func (p *EVMExplorerProvider) SupportsChain(chain string) bool {
	return chain == "eth-mainnet" || chain == "matic-mainnet"
}
func (p *EVMExplorerProvider) HealthCheck(ctx context.Context) error {
	_, err := p.fetchTokenTransfers(ctx, "eth-mainnet", "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045")
	return err
}

type evmResponse struct {
	Status  string        `json:"status"`
	Message string        `json:"message"`
	Result  []evmTransfer `json:"result"`
}
type evmTransfer struct {
	Hash            string `json:"hash"`
	TimeStamp       string `json:"timeStamp"`
	From            string `json:"from"`
	To              string `json:"to"`
	Value           string `json:"value"`
	TokenDecimal    string `json:"tokenDecimal"`
	TokenSymbol     string `json:"tokenSymbol"`
	ContractAddress string `json:"contractAddress"`
}

func (p *EVMExplorerProvider) FetchWalletTransactions(ctx context.Context, wallet models.Wallet, since time.Time, tokenFilters []string) ([]models.WalletTransaction, error) {
	events, err := p.fetchTokenTransfers(ctx, wallet.Chain, wallet.Address)
	if err != nil {
		return nil, err
	}
	filters := map[string]bool{}
	for _, t := range tokenFilters {
		filters[strings.ToUpper(strings.TrimSpace(t))] = true
	}
	addr := strings.ToLower(wallet.Address)
	out := make([]models.WalletTransaction, 0, len(events))
	for _, ev := range events {
		tsUnix, err := strconv.ParseInt(ev.TimeStamp, 10, 64)
		if err != nil {
			continue
		}
		ts := time.Unix(tsUnix, 0).UTC()
		if ts.Before(since) {
			continue
		}
		token := strings.ToUpper(strings.TrimSpace(ev.TokenSymbol))
		if token == "" {
			token = "UNKNOWN"
		}
		if len(filters) > 0 && !filters[token] {
			continue
		}
		direction := "unknown"
		from := strings.ToLower(ev.From)
		to := strings.ToLower(ev.To)
		if to == addr {
			direction = "transfer_in"
		} else if from == addr {
			direction = "transfer_out"
		}
		out = append(out, models.WalletTransaction{
			WalletID:     wallet.ID,
			TxHash:       ev.Hash,
			Chain:        wallet.Chain,
			TokenSymbol:  token,
			TokenAddress: ev.ContractAddress,
			Direction:    direction,
			Amount:       parseAmount(ev.Value, ev.TokenDecimal),
			Timestamp:    ts,
			RawPayload:   repo.EncodePayload(ev),
		})
	}
	return out, nil
}

func (p *EVMExplorerProvider) fetchTokenTransfers(ctx context.Context, chain, address string) ([]evmTransfer, error) {
	chainID := "1"
	if chain == "matic-mainnet" {
		chainID = "137"
	}
	u, _ := url.Parse("https://api.etherscan.io/v2/api")
	q := u.Query()
	q.Set("chainid", chainID)
	q.Set("module", "account")
	q.Set("action", "tokentx")
	q.Set("address", address)
	q.Set("startblock", "0")
	q.Set("endblock", "99999999")
	q.Set("sort", "asc")
	if p.apiKey != "" {
		q.Set("apikey", p.apiKey)
	}
	u.RawQuery = q.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	res, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("evm explorer status: %d", res.StatusCode)
	}
	var payload evmResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Status == "0" && strings.Contains(strings.ToLower(payload.Message), "no transactions") {
		return []evmTransfer{}, nil
	}
	return payload.Result, nil
}

func parseAmount(v, d string) float64 {
	vf, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	di, err := strconv.Atoi(d)
	if err != nil || di <= 0 {
		return vf
	}
	return vf / math.Pow10(di)
}
