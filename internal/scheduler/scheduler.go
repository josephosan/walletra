package scheduler

import (
	"context"
	"log"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"wallet_tracker_bot/internal/models"
	"wallet_tracker_bot/internal/repo"
	"wallet_tracker_bot/internal/service"
)

type Scheduler struct {
	log     *log.Logger
	repo    *repo.Repository
	tracker *service.TrackerService
	report  *service.ReportService
	bot     *tgbotapi.BotAPI
}

func New(log *log.Logger, repo *repo.Repository, tracker *service.TrackerService, report *service.ReportService, bot *tgbotapi.BotAPI) *Scheduler {
	return &Scheduler{log: log, repo: repo, tracker: tracker, report: report, bot: bot}
}

func (s *Scheduler) Start(ctx context.Context, pollInterval time.Duration) {
	pollTicker := time.NewTicker(pollInterval)
	reportTicker := time.NewTicker(10 * time.Minute)

	go func() {
		defer pollTicker.Stop()
		defer reportTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pollTicker.C:
				s.tracker.PollOnce(ctx)
			case <-reportTicker.C:
				s.dispatchReports(ctx)
			}
		}
	}()
}

func (s *Scheduler) dispatchReports(ctx context.Context) {
	now := time.Now().UTC()
	users, err := s.repo.UsersDueForReport(ctx, now)
	if err != nil {
		s.log.Printf("users due error: %v", err)
		return
	}
	for _, u := range users {
		settings, err := s.repo.GetSettings(ctx, u.ID)
		if err != nil {
			s.log.Printf("settings error user=%s err=%v", u.ID, err)
			continue
		}
		text, err := s.report.BuildReportText(ctx, u.ID, settings.ReportFrequency, settings.IncludeUnchangedWallets, now)
		if err != nil {
			s.log.Printf("report error user=%s err=%v", u.ID, err)
			continue
		}
		msg := tgbotapi.NewMessage(u.TelegramID, text)
		if _, err := s.bot.Send(msg); err != nil {
			s.log.Printf("send report error tg=%d err=%v", u.TelegramID, err)
			continue
		}
		next := service.NextRunAt(now, models.Frequency(settings.ReportFrequency))
		if err := s.repo.SetNextReportAt(ctx, u.ID, next); err != nil {
			s.log.Printf("set next run error user=%s err=%v", u.ID, err)
		}
	}
}
