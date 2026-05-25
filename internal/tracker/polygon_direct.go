package tracker

import (
	"context"
	"encoding/hex"
	"fmt"
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
)

var transferTopic = crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))

type PolygonDirectProvider struct {
	repo          *repo.Repository
	client        *ethclient.Client
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
	if cfg.Confirmations <= 0 {
		cfg.Confirmations = 20
	}
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = 500
	}
	return &PolygonDirectProvider{repo: r, client: client, rpcURL: cfg.RPCURL, startBlock: cfg.StartBlock, confirmations: cfg.Confirmations, chunkSize: cfg.ChunkSize}, nil
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
	if start > safeTo {
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
	current := start
	for current <= safeTo {
		to := current + p.chunkSize - 1
		if to > safeTo {
			to = safeTo
		}
		chunkTxs, lastHash, err := p.scanRange(ctx, wallet, walletAddr, since, current, to, filterContracts, filterSymbols)
		if err != nil {
			return nil, err
		}
		out = append(out, chunkTxs...)
		if err := p.repo.UpsertPolygonIndexerState(ctx, wallet.Chain, to, lastHash); err != nil {
			return nil, fmt.Errorf("save polygon indexer state: %w", err)
		}
		current = to + 1
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

	chainID, err := p.client.ChainID(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("get chain id: %w", err)
	}
	for bn := from; bn <= to; bn++ {
		block, err := p.client.BlockByNumber(ctx, big.NewInt(bn))
		if err != nil {
			return nil, "", fmt.Errorf("load block %d: %w", bn, err)
		}
		lastBlockHash = block.Hash().Hex()
		bt := time.Unix(int64(block.Time()), 0).UTC()
		if bt.Before(since) {
			continue
		}
		signer := types.LatestSignerForChainID(chainID)
		for txIdx, tx := range block.Transactions() {
			toAddr := tx.To()
			if toAddr == nil {
				continue
			}
			fromAddr, err := types.Sender(signer, tx)
			if err != nil {
				continue
			}
			if fromAddr != walletAddr && *toAddr != walletAddr {
				continue
			}
			direction := "unknown"
			if *toAddr == walletAddr {
				direction = "transfer_in"
			} else if fromAddr == walletAddr {
				direction = "transfer_out"
			}
			amount := weiToMatic(tx.Value())
			raw := map[string]any{
				"block_number": bn,
				"block_hash":   block.Hash().Hex(),
				"tx_index":     txIdx,
				"gas":          tx.Gas(),
				"gas_price":    tx.GasPrice().String(),
				"nonce":        tx.Nonce(),
				"from":         fromAddr.Hex(),
				"to":           toAddr.Hex(),
			}
			out = append(out, models.WalletTransaction{WalletID: wallet.ID, TxHash: tx.Hash().Hex(), Chain: wallet.Chain, TokenSymbol: "MATIC", Direction: direction, Amount: amount, Timestamp: bt, RawPayload: repo.EncodePayload(raw)})
		}
	}

	// ERC20 transfers via logs for the same block range.
	inLogs, err := p.getTransferLogs(ctx, walletAddr, true, from, to)
	if err != nil {
		return nil, "", err
	}
	outLogs, err := p.getTransferLogs(ctx, walletAddr, false, from, to)
	if err != nil {
		return nil, "", err
	}

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
	logs, err := p.client.FilterLogs(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("filter logs %d-%d: %w", from, to, err)
	}
	return logs, nil
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
