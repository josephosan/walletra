package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"walletra/internal/models"
	"walletra/internal/repo"
)

type ReportService struct {
	repo *repo.Repository
}

func NewReportService(r *repo.Repository) *ReportService { return &ReportService{repo: r} }

func (s *ReportService) TimeRange(now time.Time, freq models.Frequency) (time.Time, time.Time) {
	to := now.UTC()
	switch freq {
	case models.FreqHourly:
		return to.Add(-1 * time.Hour), to
	case models.FreqMonthly:
		return to.AddDate(0, -1, 0), to
	case models.FreqYearly:
		return to.AddDate(-1, 0, 0), to
	default:
		return to.Add(-24 * time.Hour), to
	}
}

func (s *ReportService) BuildReportText(ctx context.Context, userID string, freq models.Frequency, includeUnchanged bool, now time.Time) (string, error) {
	from, to := s.TimeRange(now, freq)
	rows, err := s.repo.AggregateReport(ctx, userID, from, to, includeUnchanged)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return fmt.Sprintf("No wallet activity for %s period (%s to %s).", freq, from.Format(time.RFC3339), to.Format(time.RFC3339)), nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s report (%s to %s)\n\n", strings.Title(string(freq)), from.Format("2006-01-02 15:04"), to.Format("2006-01-02 15:04")))
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("• %s\n", r.WalletName))
		b.WriteString(fmt.Sprintf("  address: %s\n", r.Address))
		b.WriteString(fmt.Sprintf("  chain: %s\n", r.Chain))
		b.WriteString(fmt.Sprintf("  %s %.6f %s\n\n", r.Direction, r.Amount, r.Token))
	}
	return b.String(), nil
}

func (s *ReportService) BuildWalletReportText(ctx context.Context, userID, walletID string, freq models.Frequency, includeUnchanged bool, now time.Time) (string, error) {
	from, to := s.TimeRange(now, freq)
	rows, err := s.repo.AggregateReportForWallet(ctx, userID, walletID, from, to, includeUnchanged)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return fmt.Sprintf("No wallet activity for %s period (%s to %s).", freq, from.Format(time.RFC3339), to.Format(time.RFC3339)), nil
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s wallet report (%s to %s)\n\n", strings.Title(string(freq)), from.Format("2006-01-02 15:04"), to.Format("2006-01-02 15:04")))
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("• %s\n", r.WalletName))
		b.WriteString(fmt.Sprintf("  address: %s\n", r.Address))
		b.WriteString(fmt.Sprintf("  chain: %s\n", r.Chain))
		b.WriteString(fmt.Sprintf("  %s %.6f %s\n\n", r.Direction, r.Amount, r.Token))
	}
	return b.String(), nil
}

func NextRunAt(now time.Time, freq models.Frequency) time.Time {
	n := now.UTC()
	switch freq {
	case models.FreqHourly:
		return n.Add(1 * time.Hour)
	case models.FreqMonthly:
		return n.AddDate(0, 1, 0)
	case models.FreqYearly:
		return n.AddDate(1, 0, 0)
	default:
		return n.Add(24 * time.Hour)
	}
}
