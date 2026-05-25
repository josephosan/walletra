package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"walletra/internal/models"
	"walletra/internal/repo"
)

type BTCProvider struct{ client *http.Client }

func NewBTCProvider() *BTCProvider {
	return &BTCProvider{client: &http.Client{Timeout: 20 * time.Second}}
}
func (p *BTCProvider) Name() string                    { return "btc-blockstream" }
func (p *BTCProvider) SupportsChain(chain string) bool { return chain == "btc-mainnet" }
func (p *BTCProvider) HealthCheck(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://blockstream.info/api/blocks/tip/height", nil)
	res, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("blockstream status: %d", res.StatusCode)
	}
	return nil
}

type btcTx struct {
	Txid   string `json:"txid"`
	Status struct {
		BlockTime int64 `json:"block_time"`
	} `json:"status"`
	Vin []struct {
		Prevout struct {
			ScriptPubKeyAddress string `json:"scriptpubkey_address"`
			Value               int64  `json:"value"`
		} `json:"prevout"`
	} `json:"vin"`
	Vout []struct {
		ScriptPubKeyAddress string `json:"scriptpubkey_address"`
		Value               int64  `json:"value"`
	} `json:"vout"`
}

func (p *BTCProvider) FetchWalletTransactions(ctx context.Context, wallet models.Wallet, since time.Time, _ []string) ([]models.WalletTransaction, error) {
	u := "https://blockstream.info/api/address/" + wallet.Address + "/txs"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	res, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("btc status: %d", res.StatusCode)
	}
	var txs []btcTx
	if err := json.NewDecoder(res.Body).Decode(&txs); err != nil {
		return nil, err
	}
	addr := strings.ToLower(wallet.Address)
	out := make([]models.WalletTransaction, 0, len(txs))
	for _, tx := range txs {
		ts := time.Unix(tx.Status.BlockTime, 0).UTC()
		if ts.Before(since) {
			continue
		}
		var inSat, outSat int64
		for _, vin := range tx.Vin {
			if strings.ToLower(vin.Prevout.ScriptPubKeyAddress) == addr {
				inSat += vin.Prevout.Value
			}
		}
		for _, vout := range tx.Vout {
			if strings.ToLower(vout.ScriptPubKeyAddress) == addr {
				outSat += vout.Value
			}
		}
		net := outSat - inSat
		dir := "unknown"
		if net > 0 {
			dir = "transfer_in"
		} else if net < 0 {
			dir = "transfer_out"
		}
		amt := float64(net)
		if amt < 0 {
			amt = -amt
		}
		amt = amt / 1e8
		out = append(out, models.WalletTransaction{WalletID: wallet.ID, TxHash: tx.Txid, Chain: wallet.Chain, TokenSymbol: "BTC", Direction: dir, Amount: amt, Timestamp: ts, RawPayload: repo.EncodePayload(tx)})
	}
	return out, nil
}
