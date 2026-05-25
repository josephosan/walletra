package service

import (
	"context"
	"log"
	"time"

	"walletra/internal/repo"
	"walletra/internal/tracker"
)

type TrackerService struct {
	repo       *repo.Repository
	provider   tracker.TransactionProvider
	log        *log.Logger
	pollWindow time.Duration
}

func NewTrackerService(r *repo.Repository, p tracker.TransactionProvider, l *log.Logger, pollWindow time.Duration) *TrackerService {
	return &TrackerService{repo: r, provider: p, log: l, pollWindow: pollWindow}
}

func (s *TrackerService) PollOnce(ctx context.Context) {
	wallets, err := s.repo.ListWalletsForPolling(ctx)
	if err != nil {
		s.log.Printf("poll list wallets error: %v", err)
		return
	}
	s.log.Printf("poll cycle started wallets=%d", len(wallets))
	now := time.Now().UTC()
	totalFetched := 0
	totalInserted := 0
	for _, w := range wallets {
		// Regular polling window uses configured interval.
		since := now.Add(-1 * s.pollWindow)
		// For first-time wallets, provider handles bootstrap mode itself.
		if w.LastPolledAt != nil && w.LastPolledAt.After(since) {
			since = *w.LastPolledAt
		}
		s.log.Printf("poll wallet start wallet_id=%s name=%q chain=%s since=%s", w.ID, w.Name, w.Chain, since.Format(time.RFC3339))
		filters, err := s.repo.GetWalletTokens(ctx, w.ID)
		if err != nil {
			s.log.Printf("tokens error wallet=%s err=%v", w.ID, err)
			continue
		}
		s.log.Printf("poll wallet filters wallet_id=%s filters=%v", w.ID, filters)
		txs, err := s.provider.FetchWalletTransactions(ctx, w, since, filters)
		if err != nil {
			s.log.Printf("provider error wallet=%s err=%v", w.ID, err)
			continue
		}
		totalFetched += len(txs)
		s.log.Printf("wallet polled wallet_id=%s name=%q chain=%s fetched_txs=%d since=%s", w.ID, w.Name, w.Chain, len(txs), since.Format(time.RFC3339))
		if len(txs) > 0 {
			if err := s.repo.InsertWalletTransactions(ctx, txs); err != nil {
				s.log.Printf("insert txs error wallet=%s err=%v", w.ID, err)
			} else {
				totalInserted += len(txs)
			}
		}
		if err := s.repo.MarkWalletPolled(ctx, w.ID, now); err != nil {
			s.log.Printf("mark polled error wallet=%s err=%v", w.ID, err)
		}
	}
	s.log.Printf("poll cycle completed wallets=%d fetched_txs=%d inserted_txs=%d", len(wallets), totalFetched, totalInserted)
}
