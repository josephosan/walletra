package tracker

import (
	"context"
	"fmt"
	"time"

	"walletra/internal/models"
)

type TransactionProvider interface {
	FetchWalletTransactions(ctx context.Context, wallet models.Wallet, since time.Time, tokenFilters []string) ([]models.WalletTransaction, error)
}

type ChainProvider interface {
	TransactionProvider
	SupportsChain(chain string) bool
	HealthCheck(ctx context.Context) error
	Name() string
}

type MultiChainProvider struct {
	providers []ChainProvider
}

func NewMultiChainProvider(providers ...ChainProvider) *MultiChainProvider {
	return &MultiChainProvider{providers: providers}
}

func (m *MultiChainProvider) FetchWalletTransactions(ctx context.Context, wallet models.Wallet, since time.Time, tokenFilters []string) ([]models.WalletTransaction, error) {
	for _, p := range m.providers {
		if p.SupportsChain(wallet.Chain) {
			return p.FetchWalletTransactions(ctx, wallet, since, tokenFilters)
		}
	}
	return nil, fmt.Errorf("no provider for chain: %s", wallet.Chain)
}

func (m *MultiChainProvider) ValidateAll(ctx context.Context) error {
	for _, p := range m.providers {
		if err := p.HealthCheck(ctx); err != nil {
			return fmt.Errorf("provider %s health check failed: %w", p.Name(), err)
		}
	}
	return nil
}
