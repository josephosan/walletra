package tracker

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"walletra/internal/models"
	"walletra/internal/repo"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

var transferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

type PolygonDirectProvider struct {
	repo          *repo.Repository
	client        *ethclient.Client
	rpcClient     *rpc.Client
	rpcURL        string
	startBlock    int64
	confirmations int64
	chunkSize     int64
}

type PolygonDirectConfig struct {
	RPCURL        string
	StartBlock    int64
	Confirmations int64
	ChunkSize     int64
}

func NewPolygonDirectProvider(r *repo.Repository, cfg PolygonDirectConfig) (*PolygonDirectProvider, error) {
	client, err := ethclient.Dial(cfg.RPCURL)
	if err != nil {
		return nil, fmt.Errorf("dial polygon rpc: %w", err)
	}
	rpcClient, err := rpc.Dial(cfg.RPCURL)
	if err != nil {
		return nil, fmt.Errorf("dial polygon rpc raw client: %w", err)
	}
	if cfg.Confirmations <= 0 {
		cfg.Confirmations = 20
	}
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = 500
	}
	return &PolygonDirectProvider{repo: r, client: client, rpcClient: rpcClient, rpcURL: cfg.RPCURL, startBlock: cfg.StartBlock, confirmations: cfg.Confirmations, chunkSize: cfg.ChunkSize}, nil
}

func (p *PolygonDirectProvider) Name() string                    { return "polygon-direct-rpc" }
func (p *PolygonDirectProvider) SupportsChain(chain string) bool { return chain == "matic-mainnet" }
func (p *PolygonDirectProvider) HealthCheck(ctx context.Context) error {
	if _, err := p.client.ChainID(ctx); err != nil {
		return fmt.Errorf("chain id check: %w", err)
	}
	if _, err := p.client.BlockNumber(ctx); err != nil {
		return fmt.Errorf("block number check: %w", err)
	}
	return nil
}

func (p *PolygonDirectProvider) FetchWalletTransactions(ctx context.Context, wallet models.Wallet, since time.Time, _ []string) ([]models.WalletTransaction, error) {
	if !p.SupportsChain(wallet.Chain) {
		return nil, nil
	}
	start, err := p.resolveStartBlock(ctx, wallet, since)
	if err != nil {
		return nil, err
	}
	latest, err := p.client.BlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("latest block: %w", err)
	}
	if int64(latest) <= p.confirmations {
		return nil, nil
	}
	safeTo := int64(latest) - p.confirmations
	log.Printf("[polygon-direct] wallet=%s start=%d safe_to=%d latest=%d confirmations=%d since=%s", wallet.Address, start, safeTo, latest, p.confirmations, since.Format(time.RFC3339))
	if start > safeTo {
		log.Printf("[polygon-direct] wallet=%s nothing to scan start=%d > safe_to=%d", wallet.Address, start, safeTo)
		return nil, nil
	}

	walletAddr := common.HexToAddress(wallet.Address)
	filters, err := p.repo.GetWalletTokenFilters(ctx, wallet.ID)
	if err != nil {
		return nil, fmt.Errorf("load wallet token filters: %w", err)
	}
	filterContracts := make(map[common.Address]bool)
	filterSymbols := make(map[string]bool)
	for _, f := range filters {
		s := strings.ToUpper(strings.TrimSpace(f.TokenSymbol))
		if s != "" {
			filterSymbols[s] = true
		}
		if common.IsHexAddress(strings.TrimSpace(f.TokenAddress)) {
			filterContracts[common.HexToAddress(strings.TrimSpace(f.TokenAddress))] = true
		}
	}

	out := make([]models.WalletTransaction, 0)

	// First-time wallet bootstrap: fetch latest 10 transactions only.
	if wallet.LastPolledAt == nil {
		boot, err := p.fetchLatestTen(ctx, wallet, walletAddr, safeTo, filterContracts, filterSymbols)
		if err != nil {
			return nil, err
		}
		log.Printf("[polygon-direct] wallet=%s bootstrap_latest=%d", wallet.Address, len(boot))
		return boot, nil
	}

	current := start
	for current <= safeTo {
		to := current + p.chunkSize - 1
		if to > safeTo {
			to = safeTo
		}
		log.Printf("[polygon-direct] wallet=%s scanning range=%d-%d", wallet.Address, current, to)
		chunkTxs, lastHash, err := p.scanRange(ctx, wallet, walletAddr, since, current, to, filterContracts, filterSymbols)
		if err != nil {
			return nil, err
		}
		log.Printf("[polygon-direct] wallet=%s range=%d-%d fetched=%d last_hash=%s", wallet.Address, current, to, len(chunkTxs), lastHash)
		out = append(out, chunkTxs...)
		if err := p.repo.UpsertPolygonIndexerState(ctx, wallet.Chain, to, lastHash); err != nil {
			return nil, fmt.Errorf("save polygon indexer state: %w", err)
		}
		current = to + 1
	}

	log.Printf("[polygon-direct] wallet=%s total_fetched=%d", wallet.Address, len(out))
	return out, nil
}

func (p *PolygonDirectProvider) fetchLatestTen(
	ctx context.Context,
	wallet models.Wallet,
	walletAddr common.Address,
	safeTo int64,
	filterContracts map[common.Address]bool,
	filterSymbols map[string]bool,
) ([]models.WalletTransaction, error) {
	const target = 10
	const maxBootstrapRanges = 8
	out := make([]models.WalletTransaction, 0, target)
	seen := map[string]bool{}
	lowerBound := p.startBlock
	currentTo := safeTo
	rangesScanned := 0

	for currentTo >= lowerBound && len(out) < target {
		if rangesScanned >= maxBootstrapRanges {
			log.Printf("[polygon-direct] wallet=%s bootstrap capped at %d ranges with %d txs", wallet.Address, maxBootstrapRanges, len(out))
			break
		}
		from := currentTo - p.chunkSize + 1
		if from < lowerBound {
			from = lowerBound
		}
		rangesScanned++
		chunk, _, err := p.scanRange(ctx, wallet, walletAddr, time.Unix(0, 0).UTC(), from, currentTo, filterContracts, filterSymbols)
		if err != nil {
			return nil, err
		}
		for i := len(chunk) - 1; i >= 0 && len(out) < target; i-- {
			tx := chunk[i]
			key := tx.TxHash + "|" + tx.TokenAddress + "|" + tx.Direction
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, tx)
		}
		if from == lowerBound {
			break
		}
		currentTo = from - 1
	}
	return out, nil
}

func (p *PolygonDirectProvider) resolveStartBlock(ctx context.Context, _ models.Wallet, since time.Time) (int64, error) {
	// Per-wallet scanning must use wallet-local time window; a single chain-wide
	// cursor can skip history for newly added wallets.
	latest, err := p.client.BlockNumber(ctx)
	if err != nil {
		return 0, fmt.Errorf("latest block for start resolution: %w", err)
	}
	start, err := p.findBlockByTimestamp(ctx, since.Unix(), p.startBlock, int64(latest))
	if err != nil {
		return 0, fmt.Errorf("block by timestamp: %w", err)
	}
	if start < p.startBlock {
		start = p.startBlock
	}
	return start, nil
}

func (p *PolygonDirectProvider) findBlockByTimestamp(ctx context.Context, targetTS, low, high int64) (int64, error) {
	if low < 0 {
		low = 0
	}
	best := low
	for low <= high {
		mid := (low + high) / 2
		h, err := p.client.HeaderByNumber(ctx, big.NewInt(mid))
		if err != nil {
			return 0, fmt.Errorf("header by number %d: %w", mid, err)
		}
		ts := int64(h.Time)
		if ts < targetTS {
			best = mid
			low = mid + 1
		} else if ts > targetTS {
			high = mid - 1
		} else {
			return mid, nil
		}
	}
	return best, nil
}

func (p *PolygonDirectProvider) scanRange(
	ctx context.Context,
	wallet models.Wallet,
	walletAddr common.Address,
	since time.Time,
	from, to int64,
	filterContracts map[common.Address]bool,
	filterSymbols map[string]bool,
) ([]models.WalletTransaction, string, error) {
	out := make([]models.WalletTransaction, 0)
	lastBlockHash := ""

	for bn := from; bn <= to; bn++ {
		block, err := p.getRawBlock(ctx, bn)
		if err != nil {
			return nil, "", fmt.Errorf("load block %d: %w", bn, err)
		}
		lastBlockHash = block.Hash
		bt := time.Unix(block.Timestamp, 0).UTC()
		if bt.Before(since) {
			continue
		}
		for txIdx, tx := range block.Transactions {
			if tx.To == "" {
				continue
			}
			fromAddr := strings.ToLower(tx.From)
			toAddr := strings.ToLower(tx.To)
			wAddr := strings.ToLower(walletAddr.Hex())
			if fromAddr != wAddr && toAddr != wAddr {
				continue
			}
			direction := "unknown"
			if toAddr == wAddr {
				direction = "transfer_in"
			} else if fromAddr == wAddr {
				direction = "transfer_out"
			}
			amount := hexWeiToMatic(tx.Value)
			raw := map[string]any{
				"block_number": bn,
				"block_hash":   block.Hash,
				"tx_index":     txIdx,
				"gas":          tx.Gas,
				"gas_price":    tx.GasPrice,
				"nonce":        tx.Nonce,
				"from":         tx.From,
				"to":           tx.To,
			}
			out = append(out, models.WalletTransaction{WalletID: wallet.ID, TxHash: tx.Hash, Chain: wallet.Chain, TokenSymbol: "MATIC", Direction: direction, Amount: amount, Timestamp: bt, RawPayload: repo.EncodePayload(raw)})
		}
	}
	log.Printf("[polygon-direct] wallet=%s range=%d-%d native_transfers=%d", wallet.Address, from, to, len(out))

	// ERC20 transfers via logs for the same block range.
	inLogs, err := p.getTransferLogs(ctx, walletAddr, true, from, to)
	if err != nil {
		return nil, "", err
	}
	outLogs, err := p.getTransferLogs(ctx, walletAddr, false, from, to)
	if err != nil {
		return nil, "", err
	}
	log.Printf("[polygon-direct] wallet=%s range=%d-%d erc20_logs_in=%d erc20_logs_out=%d", wallet.Address, from, to, len(inLogs), len(outLogs))

	out = append(out, p.normalizeLogs(ctx, wallet, inLogs, "transfer_in", since, filterContracts, filterSymbols)...)
	out = append(out, p.normalizeLogs(ctx, wallet, outLogs, "transfer_out", since, filterContracts, filterSymbols)...)

	return out, lastBlockHash, nil
}

func (p *PolygonDirectProvider) getTransferLogs(ctx context.Context, wallet common.Address, incoming bool, from, to int64) ([]types.Log, error) {
	topics := [][]common.Hash{{transferTopic}, nil, nil}
	if incoming {
		topics[2] = []common.Hash{common.BytesToHash(wallet.Bytes())}
	} else {
		topics[1] = []common.Hash{common.BytesToHash(wallet.Bytes())}
	}
	q := ethereum.FilterQuery{FromBlock: big.NewInt(from), ToBlock: big.NewInt(to), Topics: topics}
	return p.filterLogsChunked(ctx, q, from, to)
}

func (p *PolygonDirectProvider) filterLogsChunked(ctx context.Context, q ethereum.FilterQuery, from, to int64) ([]types.Log, error) {
	logs, err := p.client.FilterLogs(ctx, q)
	if err == nil {
		return logs, nil
	}
	msg := strings.ToLower(err.Error())
	if !(strings.Contains(msg, "block range is too large") || strings.Contains(msg, "response size exceeded")) {
		return nil, fmt.Errorf("filter logs %d-%d: %w", from, to, err)
	}
	if from >= to {
		return nil, fmt.Errorf("filter logs %d-%d failed and cannot split further: %w", from, to, err)
	}
	mid := (from + to) / 2
	leftQ := q
	rightQ := q
	leftQ.FromBlock = big.NewInt(from)
	leftQ.ToBlock = big.NewInt(mid)
	rightQ.FromBlock = big.NewInt(mid + 1)
	rightQ.ToBlock = big.NewInt(to)

	left, lerr := p.filterLogsChunked(ctx, leftQ, from, mid)
	if lerr != nil {
		return nil, lerr
	}
	right, rerr := p.filterLogsChunked(ctx, rightQ, mid+1, to)
	if rerr != nil {
		return nil, rerr
	}
	return append(left, right...), nil
}

func (p *PolygonDirectProvider) normalizeLogs(
	ctx context.Context,
	wallet models.Wallet,
	logs []types.Log,
	direction string,
	since time.Time,
	filterContracts map[common.Address]bool,
	filterSymbols map[string]bool,
) []models.WalletTransaction {
	if len(logs) == 0 {
		return nil
	}
	out := make([]models.WalletTransaction, 0, len(logs))
	seen := map[string]bool{}
	for _, lg := range logs {
		key := fmt.Sprintf("%s:%d:%s", lg.TxHash.Hex(), lg.Index, direction)
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(filterContracts) > 0 && !filterContracts[lg.Address] {
			continue
		}
		if len(lg.Topics) < 3 || len(lg.Data) < 32 {
			continue
		}
		val := new(big.Int).SetBytes(lg.Data)
		amount := tokenAmountAssume18(val)

		bt := time.Now().UTC()
		if hdr, err := p.client.HeaderByNumber(ctx, big.NewInt(int64(lg.BlockNumber))); err == nil {
			bt = time.Unix(int64(hdr.Time), 0).UTC()
		}
		if bt.Before(since) {
			continue
		}
		tokenSymbol := "ERC20"
		if len(filterSymbols) == 1 {
			for s := range filterSymbols {
				tokenSymbol = s
			}
		}
		raw := map[string]any{
			"log_index":        lg.Index,
			"tx_index":         lg.TxIndex,
			"block_number":     lg.BlockNumber,
			"block_hash":       lg.BlockHash.Hex(),
			"contract_address": lg.Address.Hex(),
			"from":             hex.EncodeToString(lg.Topics[1].Bytes()),
			"to":               hex.EncodeToString(lg.Topics[2].Bytes()),
		}
		out = append(out, models.WalletTransaction{
			WalletID:     wallet.ID,
			TxHash:       lg.TxHash.Hex(),
			Chain:        wallet.Chain,
			TokenSymbol:  tokenSymbol,
			TokenAddress: lg.Address.Hex(),
			Direction:    direction,
			Amount:       amount,
			Timestamp:    bt,
			RawPayload:   repo.EncodePayload(raw),
		})
	}
	return out
}

func weiToMatic(v *big.Int) float64 {
	f := new(big.Float).SetInt(v)
	den := new(big.Float).SetFloat64(1e18)
	out, _ := new(big.Float).Quo(f, den).Float64()
	return out
}

func tokenAmountAssume18(v *big.Int) float64 { return weiToMatic(v) }

type rpcBlock struct {
	Hash         string  `json:"hash"`
	TimestampHex string  `json:"timestamp"`
	Transactions []rpcTx `json:"transactions"`
	Timestamp    int64   `json:"-"`
}

type rpcTx struct {
	Hash     string `json:"hash"`
	From     string `json:"from"`
	To       string `json:"to"`
	Value    string `json:"value"`
	Gas      string `json:"gas"`
	GasPrice string `json:"gasPrice"`
	Nonce    string `json:"nonce"`
}

func (p *PolygonDirectProvider) getRawBlock(ctx context.Context, blockNum int64) (rpcBlock, error) {
	var out rpcBlock
	tag := fmt.Sprintf("0x%x", blockNum)
	if err := p.rpcClient.CallContext(ctx, &out, "eth_getBlockByNumber", tag, true); err != nil {
		return out, fmt.Errorf("eth_getBlockByNumber: %w", err)
	}
	ts, err := parseHexInt64(out.TimestampHex)
	if err != nil {
		return out, fmt.Errorf("parse block timestamp: %w", err)
	}
	out.Timestamp = ts
	return out, nil
}

func parseHexInt64(h string) (int64, error) {
	x := strings.TrimPrefix(strings.TrimSpace(h), "0x")
	if x == "" {
		return 0, nil
	}
	v := new(big.Int)
	if _, ok := v.SetString(x, 16); !ok {
		return 0, fmt.Errorf("invalid hex int: %s", h)
	}
	return v.Int64(), nil
}

func hexWeiToMatic(h string) float64 {
	x := strings.TrimPrefix(strings.TrimSpace(h), "0x")
	if x == "" {
		return 0
	}
	v := new(big.Int)
	if _, ok := v.SetString(x, 16); !ok {
		return 0
	}
	return weiToMatic(v)
}
