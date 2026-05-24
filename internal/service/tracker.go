package service

import (
	"context"
	"log"
	"time"

	"wallet_tracker_bot/internal/repo"
	"wallet_tracker_bot/internal/tracker"
)

type TrackerService struct {
	repo     *repo.Repository
	provider tracker.TransactionProvider
	log      *log.Logger
}

func NewTrackerService(r *repo.Repository, p tracker.TransactionProvider, l *log.Logger) *TrackerService {
	return &TrackerService{repo: r, provider: p, log: l}
}

func (s *TrackerService) PollOnce(ctx context.Context) {
	wallets, err := s.repo.ListWalletsForPolling(ctx)
	if err != nil {
		s.log.Printf("poll list wallets error: %v", err)
		return
	}
	now := time.Now().UTC()
	for _, w := range wallets {
		since := now.Add(-1 * time.Hour)
		if w.LastPolledAt != nil {
			since = *w.LastPolledAt
		}
		filters, err := s.repo.GetWalletTokens(ctx, w.ID)
		if err != nil {
			s.log.Printf("tokens error wallet=%s err=%v", w.ID, err)
			continue
		}
		txs, err := s.provider.FetchWalletTransactions(ctx, w, since, filters)
		if err != nil {
			s.log.Printf("provider error wallet=%s err=%v", w.ID, err)
			continue
		}
		if len(txs) > 0 {
			if err := s.repo.InsertWalletTransactions(ctx, txs); err != nil {
				s.log.Printf("insert txs error wallet=%s err=%v", w.ID, err)
			}
		}
		if err := s.repo.MarkWalletPolled(ctx, w.ID, now); err != nil {
			s.log.Printf("mark polled error wallet=%s err=%v", w.ID, err)
		}
	}
}
