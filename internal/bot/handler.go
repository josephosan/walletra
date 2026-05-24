package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"wallet_tracker_bot/internal/models"
	"wallet_tracker_bot/internal/repo"
	"wallet_tracker_bot/internal/service"
)

type Handler struct {
	log    *log.Logger
	repo   *repo.Repository
	report *service.ReportService
	super  int64

	mu     sync.Mutex
	states map[int64]*walletCreateState
}

type walletCreateState struct {
	Step    int
	Name    string
	Address string
	Chain   string
	Coin    string
}

func NewHandler(log *log.Logger, repo *repo.Repository, report *service.ReportService, superUserID int64) *Handler {
	return &Handler{log: log, repo: repo, report: report, super: superUserID, states: map[int64]*walletCreateState{}}
}

func (h *Handler) MainMenu() tgbotapi.ReplyKeyboardMarkup {
	return tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton("Wallets"), tgbotapi.NewKeyboardButton("Reports")),
		tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton("Settings"), tgbotapi.NewKeyboardButton("Help")),
	)
}

func (h *Handler) HandleUpdate(ctx context.Context, bot *tgbotapi.BotAPI, upd tgbotapi.Update) {
	if upd.Message != nil {
		h.handleMessage(ctx, bot, upd.Message)
		return
	}
	if upd.CallbackQuery != nil {
		h.handleCallback(ctx, bot, upd.CallbackQuery)
	}
}

func (h *Handler) handleMessage(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	username := ""
	if msg.From != nil {
		username = msg.From.UserName
	}
	u, err := h.repo.UpsertUser(ctx, msg.Chat.ID, username, msg.Chat.ID == h.super)
	if err != nil {
		h.sendText(bot, msg.Chat.ID, "Could not initialize user.")
		return
	}

	if h.handleWalletWizard(ctx, bot, msg, u.ID) {
		return
	}

	switch msg.Text {
	case "/start":
		r := tgbotapi.NewMessage(msg.Chat.ID, "Wallet Tracker Bot ready. Use buttons below.")
		r.ReplyMarkup = h.MainMenu()
		_, _ = bot.Send(r)
	case "Wallets":
		h.sendWalletMenu(bot, msg.Chat.ID)
	case "Reports":
		h.sendReportMenu(bot, msg.Chat.ID)
	case "Settings":
		h.sendSettingsMenu(bot, msg.Chat.ID)
	case "Help":
		h.sendText(bot, msg.Chat.ID, "Use Wallets to add tracked wallets, Reports for on-demand reports, Settings to control auto-report frequency and unchanged wallets visibility.")
	default:
		h.sendText(bot, msg.Chat.ID, "Use /start or the menu buttons.")
	}
}

func (h *Handler) handleWalletWizard(ctx context.Context, bot *tgbotapi.BotAPI, msg *tgbotapi.Message, userID string) bool {
	h.mu.Lock()
	st, ok := h.states[msg.Chat.ID]
	h.mu.Unlock()
	if !ok {
		return false
	}

	text := strings.TrimSpace(msg.Text)
	switch st.Step {
	case 1:
		st.Name = text
		st.Step = 2
		h.sendText(bot, msg.Chat.ID, "Send wallet address")
	case 2:
		st.Address = text
		st.Step = 3
		h.sendText(bot, msg.Chat.ID, "Send chain (example: eth-mainnet, bsc-mainnet, base-mainnet)")
	case 3:
		st.Chain = text
		st.Step = 4
		h.sendText(bot, msg.Chat.ID, "Send base coin (example: ETH, BNB)")
	case 4:
		st.Coin = text
		st.Step = 5
		h.sendText(bot, msg.Chat.ID, "Send token symbols to track (comma-separated, example: PEPE,USDT,LINK)")
	case 5:
		tokens := splitCSV(text)
		err := h.repo.AddWallet(ctx, userID, st.Name, st.Address, st.Chain, st.Coin, tokens)
		h.mu.Lock()
		delete(h.states, msg.Chat.ID)
		h.mu.Unlock()
		if err != nil {
			h.sendText(bot, msg.Chat.ID, "Failed to save wallet.")
			return true
		}
		h.sendText(bot, msg.Chat.ID, "Wallet saved and added to tracking.")
	}
	return true
}

func (h *Handler) handleCallback(ctx context.Context, bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery) {
	if cb.Message == nil {
		return
	}
	chatID := cb.Message.Chat.ID
	username := ""
	if cb.From != nil {
		username = cb.From.UserName
	}
	u, err := h.repo.UpsertUser(ctx, chatID, username, chatID == h.super)
	if err != nil {
		h.sendText(bot, chatID, "Could not initialize user.")
		return
	}

	data := cb.Data
	switch {
	case data == "wallet_add":
		h.mu.Lock()
		h.states[chatID] = &walletCreateState{Step: 1}
		h.mu.Unlock()
		h.sendText(bot, chatID, "Send wallet name")
	case data == "wallet_list":
		wallets, err := h.repo.ListWalletsByUser(ctx, u.ID)
		if err != nil {
			h.sendText(bot, chatID, "Failed to load wallets")
			break
		}
		if len(wallets) == 0 {
			h.sendText(bot, chatID, "No wallets yet.")
			break
		}
		var sb strings.Builder
		sb.WriteString("Your wallets:\n")
		for _, w := range wallets {
			sb.WriteString(fmt.Sprintf("• %s | %s | %s\n", w.Name, w.Chain, w.Address))
		}
		h.sendText(bot, chatID, sb.String())
	case strings.HasPrefix(data, "report_"):
		freq := models.Frequency(strings.TrimPrefix(data, "report_"))
		settings, err := h.repo.GetSettings(ctx, u.ID)
		if err != nil {
			h.sendText(bot, chatID, "Failed to read settings")
			break
		}
		text, err := h.report.BuildReportText(ctx, u.ID, freq, settings.IncludeUnchangedWallets, time.Now().UTC())
		if err != nil {
			h.sendText(bot, chatID, "Failed to generate report")
			break
		}
		h.sendText(bot, chatID, text)
	case strings.HasPrefix(data, "freq_"):
		freq := models.Frequency(strings.TrimPrefix(data, "freq_"))
		settings, err := h.repo.GetSettings(ctx, u.ID)
		if err == nil {
			err = h.repo.UpdateSettings(ctx, u.ID, freq, settings.IncludeUnchangedWallets)
		}
		if err != nil {
			h.sendText(bot, chatID, "Could not update frequency")
			break
		}
		h.sendText(bot, chatID, fmt.Sprintf("Report frequency changed to %s", freq))
	case data == "toggle_unchanged":
		settings, err := h.repo.GetSettings(ctx, u.ID)
		if err != nil {
			h.sendText(bot, chatID, "Could not read settings")
			break
		}
		err = h.repo.UpdateSettings(ctx, u.ID, settings.ReportFrequency, !settings.IncludeUnchangedWallets)
		if err != nil {
			h.sendText(bot, chatID, "Could not update setting")
			break
		}
		h.sendText(bot, chatID, fmt.Sprintf("include unchanged wallets: %t", !settings.IncludeUnchangedWallets))
	}

	_, _ = bot.Request(tgbotapi.NewCallback(cb.ID, "ok"))
}

func (h *Handler) sendWalletMenu(bot *tgbotapi.BotAPI, chatID int64) {
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Add Wallet", "wallet_add"),
			tgbotapi.NewInlineKeyboardButtonData("List Wallets", "wallet_list"),
		),
	)
	msg := tgbotapi.NewMessage(chatID, "Wallet menu")
	msg.ReplyMarkup = kb
	_, _ = bot.Send(msg)
}

func (h *Handler) sendReportMenu(bot *tgbotapi.BotAPI, chatID int64) {
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Hourly", "report_hourly"),
			tgbotapi.NewInlineKeyboardButtonData("Daily", "report_daily"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Monthly", "report_monthly"),
			tgbotapi.NewInlineKeyboardButtonData("Yearly", "report_yearly"),
		),
	)
	msg := tgbotapi.NewMessage(chatID, "Choose report period")
	msg.ReplyMarkup = kb
	_, _ = bot.Send(msg)
}

func (h *Handler) sendSettingsMenu(bot *tgbotapi.BotAPI, chatID int64) {
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Auto: Hourly", "freq_hourly"),
			tgbotapi.NewInlineKeyboardButtonData("Auto: Daily", "freq_daily"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Auto: Monthly", "freq_monthly"),
			tgbotapi.NewInlineKeyboardButtonData("Auto: Yearly", "freq_yearly"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Toggle Unchanged Wallets", "toggle_unchanged"),
		),
	)
	msg := tgbotapi.NewMessage(chatID, "Settings")
	msg.ReplyMarkup = kb
	_, _ = bot.Send(msg)
}

func (h *Handler) sendText(bot *tgbotapi.BotAPI, chatID int64, text string) {
	_, _ = bot.Send(tgbotapi.NewMessage(chatID, text))
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.ToUpper(strings.TrimSpace(p))
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
